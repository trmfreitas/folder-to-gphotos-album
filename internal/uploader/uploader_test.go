package uploader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestServer builds a minimal fake Google Photos API server.
// handlers maps URL path → handler function.
func newTestServer(t *testing.T, handlers map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, h := range handlers {
		mux.HandleFunc(path, h)
	}
	return httptest.NewServer(mux)
}

// makeUploader creates an Uploader that already has albumID set and a temp state file,
// pointing its httpClient at the given test server.
func makeUploader(t *testing.T, srv *httptest.Server) *Uploader {
	t.Helper()
	stateFile := filepath.Join(t.TempDir(), "state.json")
	return &Uploader{
		httpClient: srv.Client(),
		albumID:    "album-1",
		stateFile:  stateFile,
		st:         newState(),
	}
}

// writeTempFile writes content to a new temp file with the given extension
// and returns its path.
func writeTempFile(t *testing.T, ext, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*"+ext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

// ---- backoff ----

func TestBackoff(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 1 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{5, 30 * time.Second}, // capped
		{9, 30 * time.Second}, // capped
	}
	for _, tc := range cases {
		got := backoff(tc.attempt)
		if got != tc.want {
			t.Errorf("backoff(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

// ---- hashFile ----

func TestHashFile(t *testing.T) {
	path := writeTempFile(t, ".jpg", "hello world")
	h1, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	if len(h1) != 64 {
		t.Errorf("expected 64-char hex hash, got %d chars", len(h1))
	}

	// Same content → same hash.
	path2 := writeTempFile(t, ".jpg", "hello world")
	h2, _ := hashFile(path2)
	if h1 != h2 {
		t.Error("same content should produce same hash")
	}

	// Different content → different hash.
	path3 := writeTempFile(t, ".jpg", "different content")
	h3, _ := hashFile(path3)
	if h1 == h3 {
		t.Error("different content should produce different hash")
	}
}

func TestHashFileMissing(t *testing.T) {
	_, err := hashFile("/nonexistent/file.jpg")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// ---- state persistence ----

func TestSaveAndLoadState(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	u := &Uploader{stateFile: stateFile, st: newState()}
	u.st.ByHash["abc123"] = "media-id-1"
	u.st.ByPath["/foo/bar.jpg"] = "abc123"
	u.saveState()

	u2 := &Uploader{stateFile: stateFile, st: newState()}
	u2.loadState()

	if id := u2.st.ByHash["abc123"]; id != "media-id-1" {
		t.Errorf("ByHash[abc123] = %q, want media-id-1", id)
	}
	if h := u2.st.ByPath["/foo/bar.jpg"]; h != "abc123" {
		t.Errorf("ByPath[/foo/bar.jpg] = %q, want abc123", h)
	}
}

func TestLoadStateMissingFile(t *testing.T) {
	u := &Uploader{stateFile: "/nonexistent/state.json", st: newState()}
	u.loadState() // must not panic
	if len(u.st.ByHash) != 0 {
		t.Error("expected empty state when file is missing")
	}
}

func TestLoadStateInvalidJSON(t *testing.T) {
	f := filepath.Join(t.TempDir(), "state.json")
	_ = os.WriteFile(f, []byte("not json"), 0600)
	u := &Uploader{stateFile: f, st: newState()}
	u.loadState() // must not panic, should stay empty
	if len(u.st.ByHash) != 0 {
		t.Error("expected empty state after invalid JSON")
	}
}

// ---- findOrCreateAlbum ----

func TestFindOrCreateAlbum_Existing(t *testing.T) {
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/albums": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("unexpected method %s", r.Method)
			}
			resp := albumsListResponse{
				Albums: []album{
					{ID: "id-existing", Title: "My Album"},
				},
			}
			json.NewEncoder(w).Encode(resp)
		},
	})
	defer srv.Close()

	// Patch baseURL to point at test server.
	old := baseURL
	t.Cleanup(func() { _ = old }) // baseURL is const; tested via http.Client transport instead.

	// Build a client that redirects all requests to srv.
	client := redirectClient(t, srv)
	id, err := findOrCreateAlbum(context.Background(), client, "My Album")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "id-existing" {
		t.Errorf("album ID = %q, want id-existing", id)
	}
}

func TestFindOrCreateAlbum_Create(t *testing.T) {
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/albums": func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				// Return empty list — album doesn't exist yet.
				json.NewEncoder(w).Encode(albumsListResponse{})
			case http.MethodPost:
				json.NewEncoder(w).Encode(album{ID: "id-new", Title: "New Album"})
			default:
				http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			}
		},
	})
	defer srv.Close()

	client := redirectClient(t, srv)
	id, err := findOrCreateAlbum(context.Background(), client, "New Album")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "id-new" {
		t.Errorf("album ID = %q, want id-new", id)
	}
}

func TestFindOrCreateAlbum_Pagination(t *testing.T) {
	page := 0
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/albums": func(w http.ResponseWriter, r *http.Request) {
			page++
			if page == 1 {
				json.NewEncoder(w).Encode(albumsListResponse{
					Albums:        []album{{ID: "id-other", Title: "Other"}},
					NextPageToken: "tok2",
				})
			} else {
				json.NewEncoder(w).Encode(albumsListResponse{
					Albums: []album{{ID: "id-target", Title: "Target"}},
				})
			}
		},
	})
	defer srv.Close()

	client := redirectClient(t, srv)
	id, err := findOrCreateAlbum(context.Background(), client, "Target")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "id-target" {
		t.Errorf("album ID = %q, want id-target", id)
	}
}

// ---- Upload ----

func TestUpload_Success(t *testing.T) {
	photoPath := writeTempFile(t, ".jpg", "fake image data")

	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/uploads": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("upload-token-1"))
		},
		"/v1/mediaItems:batchCreate": func(w http.ResponseWriter, r *http.Request) {
			resp := batchCreateResponse{
				NewMediaItemResults: []newMediaItemResult{
					{
						Status: mediaItemStatus{Code: 0},
						MediaItem: &struct {
							ID string `json:"id"`
						}{ID: "media-abc"},
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
		},
	})
	defer srv.Close()

	u := makeUploaderWithBase(t, srv)
	if err := u.Upload(context.Background(), []string{photoPath}); err != nil {
		t.Fatalf("Upload error: %v", err)
	}

	hash, _ := hashFile(photoPath)
	if id := u.st.ByHash[hash]; id != "media-abc" {
		t.Errorf("ByHash[hash] = %q, want media-abc", id)
	}
	if h := u.st.ByPath[photoPath]; h != hash {
		t.Errorf("ByPath[path] = %q, want %q", h, hash)
	}
}

func TestUpload_SkipsDuplicate(t *testing.T) {
	photoPath := writeTempFile(t, ".jpg", "already uploaded")
	hash, _ := hashFile(photoPath)

	uploadCalls := 0
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/uploads": func(w http.ResponseWriter, r *http.Request) {
			uploadCalls++
			w.Write([]byte("tok"))
		},
		"/v1/mediaItems:batchCreate": func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(batchCreateResponse{
				NewMediaItemResults: []newMediaItemResult{{Status: mediaItemStatus{Code: 0}, MediaItem: &struct {
					ID string `json:"id"`
				}{ID: "x"}}},
			})
		},
	})
	defer srv.Close()

	u := makeUploaderWithBase(t, srv)
	u.st.ByHash[hash] = "existing-media-id"

	if err := u.Upload(context.Background(), []string{photoPath}); err != nil {
		t.Fatal(err)
	}
	if uploadCalls != 0 {
		t.Errorf("expected 0 upload calls for duplicate, got %d", uploadCalls)
	}
}

func TestUpload_UnsupportedExtensionNotFiltered(t *testing.T) {
	// The uploader itself doesn't filter by extension — the watcher does.
	// But unsupported files can still be uploaded if passed directly.
	// This test just ensures no panic.
	path := writeTempFile(t, ".xyz", "data")

	callCount := 0
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/uploads": func(w http.ResponseWriter, r *http.Request) {
			callCount++
			w.Write([]byte("tok"))
		},
		"/v1/mediaItems:batchCreate": func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(batchCreateResponse{
				NewMediaItemResults: []newMediaItemResult{{Status: mediaItemStatus{Code: 0}, MediaItem: &struct {
					ID string `json:"id"`
				}{ID: "z"}}},
			})
		},
	})
	defer srv.Close()

	u := makeUploaderWithBase(t, srv)
	if err := u.Upload(context.Background(), []string{path}); err != nil {
		t.Fatalf("Upload error: %v", err)
	}
	if callCount == 0 {
		t.Error("expected upload call for any file passed to Upload()")
	}
}

func TestUpload_BatchCreateItemError(t *testing.T) {
	photoPath := writeTempFile(t, ".jpg", "some photo")

	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/uploads": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("tok"))
		},
		"/v1/mediaItems:batchCreate": func(w http.ResponseWriter, r *http.Request) {
			resp := batchCreateResponse{
				NewMediaItemResults: []newMediaItemResult{
					{Status: mediaItemStatus{Code: 3, Message: "INVALID_ARGUMENT"}},
				},
			}
			json.NewEncoder(w).Encode(resp)
		},
	})
	defer srv.Close()

	u := makeUploaderWithBase(t, srv)
	_ = u.Upload(context.Background(), []string{photoPath})

	hash, _ := hashFile(photoPath)
	id, ok := u.st.ByHash[hash]
	if !ok {
		t.Error("failed item should be recorded in state to prevent retries")
	}
	if id != "" {
		t.Errorf("failed item should have empty media item ID, got %q", id)
	}
}

// ---- Remove ----

func TestRemove_Success(t *testing.T) {
	photoPath := writeTempFile(t, ".jpg", "to be removed")
	hash, _ := hashFile(photoPath)

	removeCalled := false
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/albums/album-1:batchRemoveMediaItems": func(w http.ResponseWriter, r *http.Request) {
			removeCalled = true
			var req batchRemoveRequest
			json.NewDecoder(r.Body).Decode(&req)
			if len(req.MediaItemIDs) != 1 || req.MediaItemIDs[0] != "media-xyz" {
				t.Errorf("unexpected mediaItemIDs: %v", req.MediaItemIDs)
			}
			w.WriteHeader(http.StatusOK)
		},
	})
	defer srv.Close()

	u := makeUploaderWithBase(t, srv)
	u.st.ByHash[hash] = "media-xyz"
	u.st.ByPath[photoPath] = hash

	if err := u.Remove(context.Background(), []string{photoPath}); err != nil {
		t.Fatalf("Remove error: %v", err)
	}
	if !removeCalled {
		t.Error("expected batchRemoveMediaItems to be called")
	}
	if _, ok := u.st.ByPath[photoPath]; ok {
		t.Error("path should be removed from state after Remove()")
	}
	if _, ok := u.st.ByHash[hash]; ok {
		t.Error("hash should be removed from state when no longer referenced")
	}
}

func TestRemove_UntrackedPathSkipped(t *testing.T) {
	removeCalled := false
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/albums/album-1:batchRemoveMediaItems": func(w http.ResponseWriter, r *http.Request) {
			removeCalled = true
		},
	})
	defer srv.Close()

	u := makeUploaderWithBase(t, srv)
	// Don't add the path to state — Remove should be a no-op.
	if err := u.Remove(context.Background(), []string{"/untracked/photo.jpg"}); err != nil {
		t.Fatalf("Remove error: %v", err)
	}
	if removeCalled {
		t.Error("batchRemoveMediaItems should not be called for untracked path")
	}
}

func TestRemove_HashStaysWhenOtherPathReferences(t *testing.T) {
	hash := "shared-hash"

	removeCalled := false
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/albums/album-1:batchRemoveMediaItems": func(w http.ResponseWriter, r *http.Request) {
			removeCalled = true
			w.WriteHeader(http.StatusOK)
		},
	})
	defer srv.Close()

	u := makeUploaderWithBase(t, srv)
	u.st.ByHash[hash] = "media-id"
	u.st.ByPath["/path/a.jpg"] = hash
	u.st.ByPath["/path/b.jpg"] = hash // second reference

	if err := u.Remove(context.Background(), []string{"/path/a.jpg"}); err != nil {
		t.Fatal(err)
	}
	if !removeCalled {
		t.Error("batchRemove should have been called")
	}
	// Hash must still be present because /path/b.jpg still references it.
	if _, ok := u.st.ByHash[hash]; !ok {
		t.Error("ByHash should be retained when another path still references the hash")
	}
}

// ---- helpers ----

// redirectClient returns an http.Client that rewrites the host of every request
// to point at the test server, while keeping the path and method intact.
func redirectClient(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	return &http.Client{
		Transport: &redirectTransport{base: http.DefaultTransport, target: srv.URL},
	}
}

type redirectTransport struct {
	base   http.RoundTripper
	target string
}

func (rt *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = rt.target[len("http://"):]
	return rt.base.RoundTrip(clone)
}

// makeUploaderWithBase creates an Uploader whose httpClient redirects to srv.
func makeUploaderWithBase(t *testing.T, srv *httptest.Server) *Uploader {
	t.Helper()
	stateFile := filepath.Join(t.TempDir(), "state.json")
	return &Uploader{
		httpClient: redirectClient(t, srv),
		albumID:    "album-1",
		stateFile:  stateFile,
		st:         newState(),
	}
}

// ---- uploadBytes ----

func TestUploadBytes_ReadError(t *testing.T) {
	srv := newTestServer(t, nil)
	defer srv.Close()

	u := makeUploaderWithBase(t, srv)
	_, err := u.uploadBytes(context.Background(), "/nonexistent/file.jpg")
	if err == nil {
		t.Error("expected error when file does not exist")
	}
}

func TestUploadBytes_NonOKStatus(t *testing.T) {
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/uploads": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("internal error"))
		},
	})
	defer srv.Close()

	u := makeUploaderWithBase(t, srv)
	path := writeTempFile(t, ".jpg", "data")
	_, err := u.uploadBytes(context.Background(), path)
	if err == nil {
		t.Error("expected error for non-200 upload response")
	}
}

func TestUploadBytes_ContextCancelled(t *testing.T) {
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/uploads": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("tok"))
		},
	})
	defer srv.Close()

	u := makeUploaderWithBase(t, srv)
	path := writeTempFile(t, ".jpg", "data")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel so httpClient.Do fails immediately

	_, err := u.uploadBytes(ctx, path)
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

// ---- batchCreate ----

func TestBatchCreate_NonOKStatus(t *testing.T) {
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/mediaItems:batchCreate": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("server error"))
		},
	})
	defer srv.Close()

	u := makeUploaderWithBase(t, srv)
	_, err := u.batchCreate(context.Background(), []newMediaItem{})
	if err == nil {
		t.Error("expected error for non-200 batchCreate response")
	}
}

func TestBatchCreate_InvalidJSON(t *testing.T) {
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/mediaItems:batchCreate": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("not json {{{"))
		},
	})
	defer srv.Close()

	u := makeUploaderWithBase(t, srv)
	_, err := u.batchCreate(context.Background(), []newMediaItem{})
	if err == nil {
		t.Error("expected error for invalid JSON batchCreate response")
	}
}

// ---- batchRemove ----

func TestBatchRemove_NonOKStatus(t *testing.T) {
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/albums/album-1:batchRemoveMediaItems": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("remove error"))
		},
	})
	defer srv.Close()

	u := makeUploaderWithBase(t, srv)
	err := u.batchRemove(context.Background(), []string{"media-id-1"})
	if err == nil {
		t.Error("expected error for non-200 batchRemove response")
	}
}

// ---- findOrCreateAlbum error paths ----

func TestFindOrCreateAlbum_ListError(t *testing.T) {
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/albums": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("list error"))
		},
	})
	defer srv.Close()

	client := redirectClient(t, srv)
	_, err := findOrCreateAlbum(context.Background(), client, "My Album")
	if err == nil {
		t.Error("expected error when albums list returns non-200")
	}
}

func TestFindOrCreateAlbum_ListInvalidJSON(t *testing.T) {
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/albums": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("not json"))
		},
	})
	defer srv.Close()

	client := redirectClient(t, srv)
	_, err := findOrCreateAlbum(context.Background(), client, "My Album")
	if err == nil {
		t.Error("expected error for invalid JSON in albums list response")
	}
}

func TestFindOrCreateAlbum_CreateError(t *testing.T) {
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/albums": func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				json.NewEncoder(w).Encode(albumsListResponse{}) // empty — triggers create
			case http.MethodPost:
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("create failed"))
			}
		},
	})
	defer srv.Close()

	client := redirectClient(t, srv)
	_, err := findOrCreateAlbum(context.Background(), client, "New Album")
	if err == nil {
		t.Error("expected error when album creation returns non-200")
	}
}

func TestFindOrCreateAlbum_CreateInvalidJSON(t *testing.T) {
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/albums": func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				json.NewEncoder(w).Encode(albumsListResponse{})
			case http.MethodPost:
				w.Write([]byte("not json"))
			}
		},
	})
	defer srv.Close()

	client := redirectClient(t, srv)
	_, err := findOrCreateAlbum(context.Background(), client, "New Album")
	if err == nil {
		t.Error("expected error for invalid JSON in album create response")
	}
}

// ---- Remove edge cases ----

func TestRemove_EmptyMediaItemID(t *testing.T) {
	removeCalled := false
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/v1/albums/album-1:batchRemoveMediaItems": func(w http.ResponseWriter, r *http.Request) {
			removeCalled = true
		},
	})
	defer srv.Close()

	u := makeUploaderWithBase(t, srv)
	u.st.ByHash["some-hash"] = "" // empty media item ID — should be skipped
	u.st.ByPath["/path/photo.jpg"] = "some-hash"

	if err := u.Remove(context.Background(), []string{"/path/photo.jpg"}); err != nil {
		t.Fatalf("Remove error: %v", err)
	}
	if removeCalled {
		t.Error("batchRemove should not be called when mediaItemID is empty")
	}
}
