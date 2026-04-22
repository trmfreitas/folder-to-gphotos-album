package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/trmfreitas/folder-to-gphotos-album/internal/config"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const tokenFile = "token.json"

// PhotosScopes are the OAuth scopes required for uploading to and removing from Google Photos.
var PhotosScopes = []string{
	"https://www.googleapis.com/auth/photoslibrary.appendonly",
	"https://www.googleapis.com/auth/photoslibrary.edit.appcreateddata",
	"https://www.googleapis.com/auth/photoslibrary.readonly.appcreateddata",
}

// Manager handles OAuth 2.0 authentication for the Google Photos API.
type Manager struct {
	oauthConfig *oauth2.Config
	tokenPath   string
}

// ClientCredentials holds the OAuth client ID and secret from Google Cloud Console.
type ClientCredentials struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// NewManager creates a new auth manager from the provided OAuth client credentials.
func NewManager(creds *ClientCredentials) (*Manager, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return nil, err
	}

	cfg := &oauth2.Config{
		ClientID:     creds.ClientID,
		ClientSecret: creds.ClientSecret,
		Scopes:       PhotosScopes,
		Endpoint:     google.Endpoint,
		RedirectURL:  "http://localhost:8085/oauth2callback",
	}

	return &Manager{
		oauthConfig: cfg,
		tokenPath:   filepath.Join(dir, tokenFile),
	}, nil
}

// NewManagerFromFile creates a Manager by reading client credentials from a JSON file
// downloaded from the Google Cloud Console (OAuth client JSON format).
func NewManagerFromFile(credFile string) (*Manager, error) {
	data, err := os.ReadFile(credFile)
	if err != nil {
		return nil, fmt.Errorf("reading credentials file: %w", err)
	}

	// Support both the raw format and the Google Console "installed" format.
	var raw struct {
		Installed *struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		} `json:"installed"`
		Web *struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		} `json:"web"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing credentials file: %w", err)
	}

	creds := &ClientCredentials{}
	switch {
	case raw.Installed != nil:
		creds.ClientID = raw.Installed.ClientID
		creds.ClientSecret = raw.Installed.ClientSecret
	case raw.Web != nil:
		creds.ClientID = raw.Web.ClientID
		creds.ClientSecret = raw.Web.ClientSecret
	default:
		creds.ClientID = raw.ClientID
		creds.ClientSecret = raw.ClientSecret
	}

	if creds.ClientID == "" || creds.ClientSecret == "" {
		return nil, fmt.Errorf("could not find client_id or client_secret in %q", credFile)
	}

	return NewManager(creds)
}

// IsAuthenticated reports whether a valid stored token exists.
func (m *Manager) IsAuthenticated() bool {
	token, err := m.loadToken()
	return err == nil && token.Valid()
}

// Setup runs the interactive OAuth browser flow, storing the token on success.
func (m *Manager) Setup(ctx context.Context) error {
	authURL := m.oauthConfig.AuthCodeURL("state", oauth2.AccessTypeOffline, oauth2.ApprovalForce)

	fmt.Println("\n=== Google Photos Authentication ===")
	fmt.Println("Opening your browser to authenticate with Google Photos.")
	fmt.Println("If the browser does not open automatically, visit this URL:")
	fmt.Println("  " + authURL)
	fmt.Println()

	// Start local callback server.
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	server := &http.Server{Addr: ":8085"}
	http.HandleFunc("/oauth2callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errCh <- fmt.Errorf("no code received from OAuth callback")
			http.Error(w, "Authentication failed. No code received.", http.StatusBadRequest)
			return
		}
		codeCh <- code
		fmt.Fprintln(w, "<html><body><h2>Authentication successful!</h2><p>You can close this tab and return to the terminal.</p></body></html>")
	})

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Try to open browser automatically.
	openBrowser(authURL)

	fmt.Println("Waiting for authentication...")

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		_ = server.Shutdown(ctx)
		return fmt.Errorf("authentication error: %w", err)
	case <-ctx.Done():
		_ = server.Shutdown(ctx)
		return ctx.Err()
	}

	_ = server.Shutdown(ctx)

	token, err := m.oauthConfig.Exchange(ctx, code)
	if err != nil {
		return fmt.Errorf("exchanging auth code: %w", err)
	}

	if err := m.saveToken(token); err != nil {
		return fmt.Errorf("saving token: %w", err)
	}

	fmt.Println("✓ Authentication successful! Token saved.")
	return nil
}

// HTTPClient returns an authenticated *http.Client ready for API calls.
// It automatically refreshes the access token when expired.
func (m *Manager) HTTPClient(ctx context.Context) (*http.Client, error) {
	token, err := m.loadToken()
	if err != nil {
		return nil, fmt.Errorf("no stored credentials; run 'folder-to-gphotos-album setup' first: %w", err)
	}

	tokenSource := m.oauthConfig.TokenSource(ctx, token)

	// Save refreshed token back to disk.
	newToken, err := tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("refreshing token: %w", err)
	}
	if newToken.AccessToken != token.AccessToken {
		if err := m.saveToken(newToken); err != nil {
			return nil, fmt.Errorf("saving refreshed token: %w", err)
		}
	}

	return oauth2.NewClient(ctx, tokenSource), nil
}

func (m *Manager) loadToken() (*oauth2.Token, error) {
	data, err := os.ReadFile(m.tokenPath)
	if err != nil {
		return nil, fmt.Errorf("reading token file: %w", err)
	}
	var token oauth2.Token
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("parsing token file: %w", err)
	}
	return &token, nil
}

func (m *Manager) saveToken(token *oauth2.Token) error {
	if err := os.MkdirAll(filepath.Dir(m.tokenPath), 0700); err != nil {
		return fmt.Errorf("creating token directory: %w", err)
	}
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding token: %w", err)
	}
	return os.WriteFile(m.tokenPath, data, 0600)
}
