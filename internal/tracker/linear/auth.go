package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

const (
	keyringService = "lanes"
	keyringUser    = "linear"
	loginTimeout   = 5 * time.Minute
)

// saved is what we persist after a successful login: the dynamically registered
// client plus the token, enough to rebuild an oauth2.Config and refresh.
type saved struct {
	ClientID     string           `json:"client_id"`
	ClientSecret string           `json:"client_secret,omitempty"`
	AuthURL      string           `json:"auth_url"`
	TokenURL     string           `json:"token_url"`
	AuthStyle    oauth2.AuthStyle `json:"auth_style"`
	RedirectURL  string           `json:"redirect_url"`
	Scopes       []string         `json:"scopes,omitempty"`
	Token        *oauth2.Token    `json:"token"`
}

func (s *saved) config() *oauth2.Config {
	return &oauth2.Config{
		ClientID: s.ClientID, ClientSecret: s.ClientSecret, RedirectURL: s.RedirectURL, Scopes: s.Scopes,
		Endpoint: oauth2.Endpoint{AuthURL: s.AuthURL, TokenURL: s.TokenURL, AuthStyle: s.AuthStyle},
	}
}

// Store persists credentials in the OS keychain, falling back to a 0600 file.
type Store struct{ File string }

func (st Store) load() (*saved, error) {
	b, err := keyringGet()
	if err != nil {
		if b, err = os.ReadFile(st.File); errors.Is(err, os.ErrNotExist) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
	}
	var s saved
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("linear: stored credential is corrupt (run `lanes auth logout`): %w", err)
	}
	return &s, nil
}

func (st Store) save(s *saved) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if keyring.Set(keyringService, keyringUser, string(b)) == nil {
		os.Remove(st.File) // keychain wins; don't leave a stale copy on disk
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(st.File), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(st.File, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(st.File, 0o600) // WriteFile keeps the mode of an existing file
}

// Delete removes stored credentials from both places.
func (st Store) Delete() error {
	kerr := keyring.Delete(keyringService, keyringUser)
	ferr := os.Remove(st.File)
	if errors.Is(kerr, keyring.ErrNotFound) {
		kerr = nil
	}
	if errors.Is(ferr, os.ErrNotExist) {
		ferr = nil
	}
	return errors.Join(kerr, ferr)
}

func keyringGet() ([]byte, error) {
	v, err := keyring.Get(keyringService, keyringUser)
	return []byte(v), err
}

// savingSource re-persists the token whenever it changes (e.g. after a refresh).
type savingSource struct {
	mu    sync.Mutex
	src   oauth2.TokenSource
	store Store
	s     *saved
}

func (ss *savingSource) Token() (*oauth2.Token, error) {
	t, err := ss.src.Token()
	if err != nil {
		return nil, err
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.s.Token == nil || t.AccessToken != ss.s.Token.AccessToken {
		ss.s.Token = t
		if err := ss.store.save(ss.s); err != nil {
			fmt.Fprintf(os.Stderr, "lanes: could not save refreshed Linear token: %v\n", err)
		}
	}
	return t, nil
}

// NewOAuth returns an OAuth handler for Linear's MCP server: it reuses stored
// credentials and otherwise runs a browser login with dynamic client registration.
// ponytail: login can trigger mid-TUI if the refresh token dies; prompt is printed to stderr.
func NewOAuth(ctx context.Context, st Store) (auth.OAuthHandler, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	cfg := &auth.AuthorizationCodeHandlerConfig{
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{
			Metadata: &oauthex.ClientRegistrationMetadata{
				ClientName:              "lanes",
				RedirectURIs:            []string{redirect},
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ResponseTypes:           []string{"code"},
				TokenEndpointAuthMethod: "none",
			},
		},
		RedirectURL:              redirect,
		RequestRefreshToken:      true,
		AuthorizationCodeFetcher: browserLogin(port),
		NewTokenSource: func(ctx context.Context, c *oauth2.Config, t *oauth2.Token) (oauth2.TokenSource, error) {
			s := &saved{
				ClientID: c.ClientID, ClientSecret: c.ClientSecret, AuthURL: c.Endpoint.AuthURL,
				TokenURL: c.Endpoint.TokenURL, AuthStyle: c.Endpoint.AuthStyle,
				RedirectURL: c.RedirectURL, Scopes: c.Scopes, Token: t,
			}
			if err := st.save(s); err != nil {
				return nil, fmt.Errorf("linear: saving credentials: %w", err)
			}
			return &savingSource{src: c.TokenSource(ctx, t), store: st, s: s}, nil
		},
	}
	s, err := st.load()
	if err != nil {
		return nil, err
	}
	if s != nil && s.Token != nil {
		// Context outlives this call: the source refreshes for the whole session.
		cfg.InitialTokenSource = &savingSource{src: s.config().TokenSource(context.WithoutCancel(ctx), s.Token), store: st, s: s}
	}
	return auth.NewAuthorizationCodeHandler(cfg)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// browserLogin opens the authorization URL and waits for the loopback redirect.
func browserLogin(port int) auth.AuthorizationCodeFetcher {
	return func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return nil, fmt.Errorf("linear: login callback port %d busy: %w", port, err)
		}
		res := make(chan *auth.AuthorizationResult, 1)
		srv := &http.Server{Handler: callback(res)}
		go srv.Serve(l)
		defer srv.Close()

		fmt.Fprintf(os.Stderr, "lanes: opening your browser to sign in to Linear.\nIf it does not open, visit:\n  %s\n", args.URL)
		openBrowser(args.URL)

		ctx, cancel := context.WithTimeout(ctx, loginTimeout)
		defer cancel()
		select {
		case r := <-res:
			if r.Code == "" {
				return nil, errors.New("linear: login was not completed")
			}
			return r, nil
		case <-ctx.Done():
			return nil, fmt.Errorf("linear: login timed out: %w", ctx.Err())
		}
	}
}

// callback handles the loopback redirect and delivers the first result to res.
func callback(res chan<- *auth.AuthorizationResult) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if e := q.Get("error"); e != "" {
			io.WriteString(w, "lanes: Linear login failed: "+e+". You can close this tab.")
		} else {
			io.WriteString(w, "lanes: signed in to Linear. You can close this tab.")
		}
		select {
		case res <- &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}:
		default:
		}
	})
}

func openBrowser(url string) {
	cmd := "xdg-open"
	if runtime.GOOS == "darwin" {
		cmd = "open"
	}
	exec.Command(cmd, url).Start()
}
