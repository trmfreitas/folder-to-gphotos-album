package uploader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	baseURL        = "https://photoslibrary.googleapis.com/v1"
	uploadEndpoint = baseURL + "/uploads"
	maxRetries     = 3
)

// ---- REST types ----

type album struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type albumsListResponse struct {
	Albums        []album `json:"albums"`
	NextPageToken string  `json:"nextPageToken"`
}

type simpleMediaItem struct {
	UploadToken string `json:"uploadToken"`
	FileName    string `json:"fileName"`
}

type newMediaItem struct {
	SimpleMediaItem simpleMediaItem `json:"simpleMediaItem"`
}

type batchCreateRequest struct {
	AlbumID       string         `json:"albumId"`
	NewMediaItems []newMediaItem `json:"newMediaItems"`
}

type mediaItemStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type newMediaItemResult struct {
	UploadToken string          `json:"uploadToken"`
	Status      mediaItemStatus `json:"status"`
	MediaItem   *struct {
		ID string `json:"id"`
	} `json:"mediaItem"`
}

type batchCreateResponse struct {
	NewMediaItemResults []newMediaItemResult `json:"newMediaItemResults"`
}

type batchRemoveRequest struct {
	MediaItemIDs []string `json:"mediaItemIds"`
}

// ---- State ----

// state tracks uploaded files so we can deduplicate and sync removals.
type state struct {
	// ByHash maps SHA256 hex digest → Google Photos media item ID.
	ByHash map[string]string `json:"byHash"`
	// ByPath maps absolute file path → SHA256 hex digest.
	ByPath map[string]string `json:"byPath"`
}

func newState() state {
	return state{
		ByHash: make(map[string]string),
		ByPath: make(map[string]string),
	}
}

// ---- Uploader ----

// Uploader manages uploading and removing files in a Google Photos album.
type Uploader struct {
	httpClient *http.Client
	albumID    string
	stateFile  string
	st         state
}

// New creates an Uploader for the given album name, creating the album if needed.
func New(ctx context.Context, httpClient *http.Client, albumName string, stateFile string) (*Uploader, error) {
	albumID, err := findOrCreateAlbum(ctx, httpClient, albumName)
	if err != nil {
		return nil, fmt.Errorf("resolving album %q: %w", albumName, err)
	}
	log.Printf("[uploader] using album %q (id=%s)", albumName, albumID)

	u := &Uploader{
		httpClient: httpClient,
		albumID:    albumID,
		stateFile:  stateFile,
		st:         newState(),
	}
	u.loadState()
	return u, nil
}

// TrackedPaths returns all file paths currently tracked in the upload state.
func (u *Uploader) TrackedPaths() []string {
	paths := make([]string, 0, len(u.st.ByPath))
	for p := range u.st.ByPath {
		paths = append(paths, p)
	}
	return paths
}

// Upload uploads the given file paths, skipping those already in the album.
func (u *Uploader) Upload(ctx context.Context, paths []string) error {
	type pendingItem struct {
		path  string
		hash  string
		token string
	}

	var items []pendingItem

	for _, path := range paths {
		hash, err := hashFile(path)
		if err != nil {
			log.Printf("[uploader] skipping %s: hash error: %v", path, err)
			continue
		}
		if _, exists := u.st.ByHash[hash]; exists {
			log.Printf("[uploader] skipping (already uploaded): %s", path)
			// Ensure path mapping is up-to-date (e.g. file was moved).
			u.st.ByPath[path] = hash
			continue
		}
		token, err := u.uploadBytes(ctx, path)
		if err != nil {
			log.Printf("[uploader] failed to upload bytes for %s: %v", path, err)
			continue
		}
		items = append(items, pendingItem{path: path, hash: hash, token: token})
	}

	if len(items) == 0 {
		return nil
	}

	const batchSize = 50
	for i := 0; i < len(items); i += batchSize {
		end := i + batchSize
		if end > len(items) {
			end = len(items)
		}
		batch := items[i:end]

		newItems := make([]newMediaItem, len(batch))
		for j, item := range batch {
			newItems[j] = newMediaItem{
				SimpleMediaItem: simpleMediaItem{
					UploadToken: item.token,
					FileName:    filepath.Base(item.path),
				},
			}
		}

		var resp batchCreateResponse
		var batchErr error
		for attempt := 0; attempt < maxRetries; attempt++ {
			resp, batchErr = u.batchCreate(ctx, newItems)
			if batchErr == nil {
				break
			}
			log.Printf("[uploader] batch create attempt %d failed: %v", attempt+1, batchErr)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}
		if batchErr != nil {
			log.Printf("[uploader] batch create failed after %d attempts: %v", maxRetries, batchErr)
			continue
		}

		for k, result := range resp.NewMediaItemResults {
			if result.Status.Code != 0 {
				log.Printf("[uploader] item %s failed (code %d): %s", batch[k].path, result.Status.Code, result.Status.Message)
				continue
			}
			id := ""
			if result.MediaItem != nil {
				id = result.MediaItem.ID
			}
			log.Printf("[uploader] uploaded: %s id=%s", batch[k].path, id)
			u.st.ByHash[batch[k].hash] = id
			u.st.ByPath[batch[k].path] = batch[k].hash
		}
		u.saveState()
	}

	return nil
}

// Remove removes the given file paths from the album.
// Files not previously uploaded by this daemon are silently skipped.
func (u *Uploader) Remove(ctx context.Context, paths []string) error {
	type removeItem struct {
		path        string
		hash        string
		mediaItemID string
	}

	var items []removeItem
	for _, path := range paths {
		hash, ok := u.st.ByPath[path]
		if !ok {
			log.Printf("[uploader] remove skipped (not tracked): %s", path)
			continue
		}
		mediaItemID, ok := u.st.ByHash[hash]
		if !ok || mediaItemID == "" {
			log.Printf("[uploader] remove skipped (no media item ID): %s", path)
			continue
		}
		items = append(items, removeItem{path: path, hash: hash, mediaItemID: mediaItemID})
	}

	if len(items) == 0 {
		return nil
	}

	const batchSize = 50
	for i := 0; i < len(items); i += batchSize {
		end := i + batchSize
		if end > len(items) {
			end = len(items)
		}
		batch := items[i:end]

		ids := make([]string, len(batch))
		for j, item := range batch {
			ids[j] = item.mediaItemID
		}

		var removeErr error
		for attempt := 0; attempt < maxRetries; attempt++ {
			removeErr = u.batchRemove(ctx, ids)
			if removeErr == nil {
				break
			}
			log.Printf("[uploader] batch remove attempt %d failed: %v", attempt+1, removeErr)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}
		if removeErr != nil {
			log.Printf("[uploader] batch remove failed after %d attempts: %v", maxRetries, removeErr)
			continue
		}

		for _, item := range batch {
			log.Printf("[uploader] removed from album: %s (id=%s)", item.path, item.mediaItemID)
			delete(u.st.ByPath, item.path)
			// Only delete the hash entry if no other tracked path still references it.
			stillReferenced := false
			for _, h := range u.st.ByPath {
				if h == item.hash {
					stillReferenced = true
					break
				}
			}
			if !stillReferenced {
				delete(u.st.ByHash, item.hash)
			}
		}
		u.saveState()
	}

	return nil
}

// mimeTypes provides MIME types for extensions not reliably present in the
// system MIME database (e.g. .heic/.heif on macOS/Linux).
var mimeTypes = map[string]string{
	".heic": "image/heic",
	".heif": "image/heif",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".tiff": "image/tiff",
	".tif":  "image/tiff",
	".mp4":  "video/mp4",
	".mov":  "video/quicktime",
	".avi":  "video/x-msvideo",
}

func mimeTypeForFile(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if t, ok := mimeTypes[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}
func (u *Uploader) uploadBytes(ctx context.Context, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}

	mimeType := mimeTypeForFile(path)

	var token string
	for attempt := 0; attempt < maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadEndpoint, bytes.NewReader(data))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Goog-Upload-Content-Type", mimeType)
		req.Header.Set("X-Goog-Upload-Protocol", "raw")
		req.Header.Set("X-Goog-Upload-File-Name", filepath.Base(path))

		resp, err := u.httpClient.Do(req)
		if err != nil {
			log.Printf("[uploader] upload attempt %d error: %v", attempt+1, err)
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(backoff(attempt)):
				continue
			}
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests {
			log.Printf("[uploader] rate limited, backing off...")
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(backoff(attempt)):
				continue
			}
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("upload returned status %d: %s", resp.StatusCode, string(body))
		}

		token = string(body)
		break
	}

	if token == "" {
		return "", fmt.Errorf("failed to obtain upload token for %s after %d attempts", path, maxRetries)
	}
	return token, nil
}

// batchCreate calls the mediaItems:batchCreate endpoint.
func (u *Uploader) batchCreate(ctx context.Context, items []newMediaItem) (batchCreateResponse, error) {
	reqBody, err := json.Marshal(batchCreateRequest{
		AlbumID:       u.albumID,
		NewMediaItems: items,
	})
	if err != nil {
		return batchCreateResponse{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/mediaItems:batchCreate", bytes.NewReader(reqBody))
	if err != nil {
		return batchCreateResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return batchCreateResponse{}, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return batchCreateResponse{}, fmt.Errorf("batchCreate status %d: %s", resp.StatusCode, string(respBody))
	}

	var result batchCreateResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return batchCreateResponse{}, fmt.Errorf("decoding batchCreate response: %w", err)
	}
	return result, nil
}

// batchRemove calls the albums.batchRemoveMediaItems endpoint.
func (u *Uploader) batchRemove(ctx context.Context, mediaItemIDs []string) error {
	reqBody, err := json.Marshal(batchRemoveRequest{MediaItemIDs: mediaItemIDs})
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/albums/%s:batchRemoveMediaItems", baseURL, u.albumID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("batchRemove status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// loadState reads previously uploaded file state from disk.
func (u *Uploader) loadState() {
	data, err := os.ReadFile(u.stateFile)
	if err != nil {
		return
	}
	st := newState()
	if err := json.Unmarshal(data, &st); err != nil {
		log.Printf("[uploader] warning: could not parse state file, starting fresh: %v", err)
		return
	}
	u.st = st
	log.Printf("[uploader] loaded state: %d hashes, %d path mappings", len(u.st.ByHash), len(u.st.ByPath))
}

// saveState persists the current state to disk.
func (u *Uploader) saveState() {
	data, err := json.MarshalIndent(u.st, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(u.stateFile), 0700); err != nil {
		return
	}
	_ = os.WriteFile(u.stateFile, data, 0600)
}

// findOrCreateAlbum returns the album ID for the given name, creating one if needed.
func findOrCreateAlbum(ctx context.Context, client *http.Client, name string) (string, error) {
	var pageToken string
	for {
		url := baseURL + "/albums?pageSize=50"
		if pageToken != "" {
			url += "&pageToken=" + pageToken
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("listing albums: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("albums list status %d: %s", resp.StatusCode, string(body))
		}

		var listResp albumsListResponse
		if err := json.Unmarshal(body, &listResp); err != nil {
			return "", err
		}
		for _, a := range listResp.Albums {
			if a.Title == name {
				return a.ID, nil
			}
		}
		if listResp.NextPageToken == "" {
			break
		}
		pageToken = listResp.NextPageToken
	}

	payload, _ := json.Marshal(map[string]any{
		"album": map[string]string{"title": name},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/albums", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("creating album: %w", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("create album status %d: %s", resp.StatusCode, string(body))
	}

	var created album
	if err := json.Unmarshal(body, &created); err != nil {
		return "", err
	}
	log.Printf("[uploader] created new album %q (id=%s)", name, created.ID)
	return created.ID, nil
}

// hashFile returns the SHA256 hex digest of the file at path.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// backoff returns an exponential wait duration for the given attempt number.
func backoff(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt)) * time.Second
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}
