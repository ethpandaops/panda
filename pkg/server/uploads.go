package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ethpandaops/panda/pkg/attribution"
	"github.com/ethpandaops/panda/pkg/proxy"
)

// maxLocalUploadBytes caps a single upload held in memory.
const maxLocalUploadBytes int64 = 100 << 20

// uploadMaxItems bounds how many private uploads the store keeps; the oldest is
// evicted past this. Private uploads live only in memory for the server's
// lifetime ("just for the session") — nothing touches disk or leaves the machine
// until the user publishes.
const uploadMaxItems = 32

type uploadItem struct {
	name        string
	contentType string
	data        []byte
	seq         uint64
}

type uploadStore struct {
	mu    sync.Mutex
	items map[string]*uploadItem
	seq   uint64
}

func newUploadStore() *uploadStore {
	return &uploadStore{items: make(map[string]*uploadItem, uploadMaxItems)}
}

func (s *uploadStore) put(name, contentType string, data []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	id := uuid.NewString()
	s.items[id] = &uploadItem{name: name, contentType: contentType, data: data, seq: s.seq}

	for len(s.items) > uploadMaxItems {
		oldestID, oldestSeq := "", ^uint64(0)
		for k, v := range s.items {
			if v.seq < oldestSeq {
				oldestID, oldestSeq = k, v.seq
			}
		}

		delete(s.items, oldestID)
	}

	return id
}

func (s *uploadStore) get(id string) (*uploadItem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	it, ok := s.items[id]

	return it, ok
}

// uploadStoredResponse is returned by POST /api/v1/uploads. The file is private
// and in-memory at this point; PreviewPath points at the local preview page.
type uploadStoredResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	PreviewPath string `json:"preview_path"`
}

func uploadName(raw string) string {
	name := path.Base(strings.TrimSpace(raw))
	if name == "" || name == "." || name == ".." || name == "/" {
		return "file"
	}

	return name
}

func uploadContentType(name string) string {
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		return ct
	}

	return "application/octet-stream"
}

// handleUpload buffers a file into the in-memory session store and returns a
// private preview link. Nothing leaves the machine until the user publishes.
func (s *service) handleUpload(w http.ResponseWriter, r *http.Request) {
	name := uploadName(r.URL.Query().Get("name"))

	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLocalUploadBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("file exceeds %d bytes", maxLocalUploadBytes))
			return
		}

		writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("reading body: %v", err))

		return
	}

	if len(data) == 0 {
		writeAPIError(w, http.StatusBadRequest, "empty body")
		return
	}

	ct := uploadContentType(name)
	id := s.uploads.put(name, ct, data)

	writeJSON(w, http.StatusOK, uploadStoredResponse{
		ID:          id,
		Name:        name,
		ContentType: ct,
		PreviewPath: "/u/" + id,
	})
}

// handleUploadServeRaw serves a private upload's bytes for the preview page. A
// sandbox CSP ensures uploaded HTML can never execute scripts in the server's
// origin, even if opened directly.
func (s *service) handleUploadServeRaw(w http.ResponseWriter, r *http.Request) {
	it, ok := s.uploads.get(chi.URLParam(r, "id"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", it.contentType)
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(it.data)
}

// handleUploadPublish streams a stored private upload to the proxy's /uploads
// route (which holds the R2 credentials), making it public.
func (s *service) handleUploadPublish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.ID) == "" {
		writeAPIError(w, http.StatusBadRequest, "id is required")
		return
	}

	it, ok := s.uploads.get(req.ID)
	if !ok {
		writeAPIError(w, http.StatusNotFound, "upload not found")
		return
	}

	proxySvc, err := s.primaryProxyService()
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, fmt.Sprintf("publishing unavailable: %v", err))
		return
	}

	target := strings.TrimRight(proxySvc.URL(), "/") + "/uploads"

	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(it.data))
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	proxyReq.URL.RawQuery = "name=" + url.QueryEscape(it.name)
	proxyReq.ContentLength = int64(len(it.data))
	proxyReq.Header.Set("Content-Type", "application/octet-stream")

	if v := r.Header.Get(attribution.Header); v != "" {
		proxyReq.Header.Set(attribution.Header, v)
	}

	if token := proxySvc.RegisterToken(); token != "" && token != proxy.NoAuthToken {
		proxyReq.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(proxyReq)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, fmt.Sprintf("publish failed: %v", err))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	data, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(data)
}

// handleUploadPreviewPage renders the private preview page with a publish button.
func (s *service) handleUploadPreviewPage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	it, ok := s.uploads.get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}

	ct := it.contentType

	data := uploadPreviewData{
		ID:      id,
		Name:    it.name,
		RawURL:  "/u/" + id + "/raw",
		IsImage: strings.HasPrefix(ct, "image/") && ct != "image/svg+xml",
		IsFrame: strings.HasPrefix(ct, "application/pdf") || strings.HasPrefix(ct, "text/"),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := uploadPreviewTmpl.Execute(w, data); err != nil {
		s.log.WithError(err).Warn("rendering upload preview")
	}
}

type uploadPreviewData struct {
	ID      string
	Name    string
	RawURL  string
	IsImage bool
	IsFrame bool
}

var uploadPreviewTmpl = template.Must(template.New("preview").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>panda upload — {{.Name}}</title>
<style>
 body{font-family:system-ui,-apple-system,sans-serif;max-width:720px;margin:3rem auto;padding:0 1rem;color:#111}
 h2{word-break:break-all}
 .card{border:1px solid #ddd;border-radius:12px;padding:1.25rem}
 .preview img,.preview iframe{max-width:100%;border-radius:8px;border:1px solid #eee}
 .preview iframe{width:100%;height:60vh}
 .meta{color:#666;font-size:.9rem;margin:.5rem 0}
 button{background:#111;color:#fff;border:0;border-radius:8px;padding:.6rem 1rem;font-size:1rem;cursor:pointer}
 button:disabled{opacity:.5;cursor:default}
 .url{margin-top:1rem;padding:.6rem;background:#f5f5f5;border-radius:8px;word-break:break-all;font-family:monospace}
 a{color:#2563eb}
</style>
</head>
<body>
<h2>{{.Name}}</h2>
<div class="card">
 <div class="preview">
 {{if .IsImage}}<img src="{{.RawURL}}" alt="{{.Name}}">
 {{else if .IsFrame}}<iframe src="{{.RawURL}}" title="{{.Name}}" sandbox></iframe>
 {{else}}<a href="{{.RawURL}}" download="{{.Name}}">Download {{.Name}}</a>{{end}}
 </div>
 <p class="meta">Private · in memory on your machine for this session only.</p>
 <button id="pub" data-id="{{.ID}}">Make public</button>
 <div id="out"></div>
</div>
<script>
const btn=document.getElementById('pub'),out=document.getElementById('out');
btn.addEventListener('click',async()=>{
 btn.disabled=true;btn.textContent='Publishing…';
 try{
  const r=await fetch('/api/v1/uploads/publish',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({id:btn.dataset.id})});
  if(!r.ok){throw new Error((await r.text())||('HTTP '+r.status))}
  const d=await r.json();
  out.innerHTML='<p class="meta">Public · expires in 60 days</p><div class="url"><a href="'+d.url+'">'+d.url+'</a></div>';
  btn.remove();
 }catch(e){btn.disabled=false;btn.textContent='Make public';out.innerHTML='<p class="meta" style="color:#b00">'+e.message+'</p>'}
});
</script>
</body>
</html>`))
