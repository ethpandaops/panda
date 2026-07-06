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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// maxLocalUploadBytes caps a single upload held in memory.
const maxLocalUploadBytes int64 = 100 << 20

// Private uploads live only in memory for the server's lifetime ("just for the
// session") — nothing touches disk or leaves the machine until the user
// publishes. The oldest are evicted past either bound, so worst-case memory is
// capped regardless of upload size, and previews unused for uploadTTL are freed
// by a background sweeper so an idle server doesn't pin memory indefinitely.
const (
	uploadMaxItems            = 32
	uploadMaxTotalBytes int64 = 256 << 20 // 256 MiB across all held previews
	uploadTTL                 = time.Hour
	uploadSweepInterval       = 5 * time.Minute
)

type uploadItem struct {
	name        string
	contentType string
	data        []byte
	seq         uint64
	lastAccess  time.Time
}

type uploadStore struct {
	mu            sync.Mutex
	items         map[string]*uploadItem
	seq           uint64
	totalBytes    int64
	maxItems      int
	maxTotalBytes int64
	ttl           time.Duration
}

func newUploadStore() *uploadStore {
	return &uploadStore{
		items:         make(map[string]*uploadItem, uploadMaxItems),
		maxItems:      uploadMaxItems,
		maxTotalBytes: uploadMaxTotalBytes,
		ttl:           uploadTTL,
	}
}

func (s *uploadStore) put(name, contentType string, data []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	id := uuid.NewString()
	s.items[id] = &uploadItem{name: name, contentType: contentType, data: data, seq: s.seq, lastAccess: time.Now()}
	s.totalBytes += int64(len(data))

	// Evict oldest past either bound, but never the item we just added.
	for len(s.items) > 1 && (len(s.items) > s.maxItems || s.totalBytes > s.maxTotalBytes) {
		oldestID, oldestSeq := "", ^uint64(0)
		for k, v := range s.items {
			if v.seq < oldestSeq {
				oldestID, oldestSeq = k, v.seq
			}
		}

		s.totalBytes -= int64(len(s.items[oldestID].data))
		delete(s.items, oldestID)
	}

	return id
}

func (s *uploadStore) get(id string) (*uploadItem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	it, ok := s.items[id]
	if ok {
		it.lastAccess = time.Now()
	}

	return it, ok
}

// sweep frees previews that have gone unused for the TTL.
func (s *uploadStore) sweep(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, it := range s.items {
		if now.Sub(it.lastAccess) > s.ttl {
			s.totalBytes -= int64(len(it.data))
			delete(s.items, id)
		}
	}
}

// sweeper periodically frees expired previews until done closes.
func (s *uploadStore) sweeper(done <-chan struct{}) {
	t := time.NewTicker(uploadSweepInterval)
	defer t.Stop()

	for {
		select {
		case <-done:
			return
		case now := <-t.C:
			s.sweep(now)
		}
	}
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

	// Markdown diagnostics are far easier to read rendered, so convert to a
	// self-contained, sanitized HTML document up front. Everything downstream
	// (preview, publish) then treats it as ordinary HTML.
	if isMarkdown(name) {
		htmlName, htmlData, err := renderMarkdown(name, data)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("rendering markdown: %v", err))
			return
		}

		name, data, ct = htmlName, htmlData, "text/html; charset=utf-8"
	}

	id := s.uploads.put(name, ct, data)

	writeJSON(w, http.StatusOK, uploadStoredResponse{
		ID:          id,
		Name:        name,
		ContentType: ct,
		PreviewPath: "/u/" + id,
	})
}

// previewCSP mirrors the panda-uploads Worker's serving policy so the preview
// renders exactly as the published object will: HTML gets a scripted opaque
// origin, SVG/XML render script-less, inert types get no sandbox. Unlike the
// Worker, raw previews are served from the server's own origin, so HTML
// additionally gets connect-src/form-action 'none' — a hostile document must
// not be able to call localhost APIs (e.g. self-publish) before the user
// clicks "Make public".
func previewCSP(contentType string) string {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}

	switch ct {
	case "text/html":
		return "sandbox allow-scripts allow-downloads; connect-src 'none'; form-action 'none'"
	case "image/svg+xml", "application/xhtml+xml", "application/xml", "text/xml":
		return "sandbox"
	default:
		return ""
	}
}

// handleUploadServeRaw serves a private upload's bytes for the preview page
// under the same policy the published Worker URL will apply, so the preview is
// a faithful render of the eventual public page.
func (s *service) handleUploadServeRaw(w http.ResponseWriter, r *http.Request) {
	it, ok := s.uploads.get(chi.URLParam(r, "id"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", it.contentType)
	if csp := previewCSP(it.contentType); csp != "" {
		w.Header().Set("Content-Security-Policy", csp)
	}
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

	// Route through the shared proxy helper so publish gets the same treatment as
	// every other proxy-bound request: s.httpClient (User-Agent), attribution
	// forwarding, and the 401/403 invalidate-and-retry.
	requestPath := "/uploads?name=" + url.QueryEscape(it.name)
	headers := http.Header{"Content-Type": {"application/octet-stream"}}

	data, status, header, err := s.proxyRequest(r.Context(), http.MethodPost, requestPath, bytes.NewReader(it.data), headers)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, fmt.Sprintf("publish failed: %v", err))
		return
	}

	if ct := header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}

	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// handleUploadListPublished relays the proxy's authenticated published-uploads
// listing back to the CLI.
func (s *service) handleUploadListPublished(w http.ResponseWriter, r *http.Request) {
	s.relayUploadsRequest(w, r, http.MethodGet, "/uploads")
}

// handleUploadDeletePublished relays a published-upload delete (?key=) to the proxy.
func (s *service) handleUploadDeletePublished(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if key == "" {
		writeAPIError(w, http.StatusBadRequest, "key is required")
		return
	}

	s.relayUploadsRequest(w, r, http.MethodDelete, "/uploads?key="+url.QueryEscape(key))
}

func (s *service) relayUploadsRequest(w http.ResponseWriter, r *http.Request, method, requestPath string) {
	data, status, header, err := s.proxyRequest(r.Context(), method, requestPath, nil, nil)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, fmt.Sprintf("proxy request failed: %v", err))
		return
	}

	if ct := header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}

	w.WriteHeader(status)
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

// The preview page is the published page plus an injected header bar: the
// content fills the viewport and renders under the same policy the Worker will
// apply, so what the user sees is exactly what "Make public" will publish.
var uploadPreviewTmpl = template.Must(template.New("preview").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>panda upload — {{.Name}}</title>
<style>
 html,body{height:100%;margin:0}
 body{display:flex;flex-direction:column;font-family:system-ui,-apple-system,sans-serif;color:#111}
 header{flex:none;display:flex;align-items:center;gap:1rem;padding:.8rem 1.25rem;border-bottom:1px solid #ddd;background:#fafafa;font-size:1.05rem}
 .name{font-weight:600;word-break:break-all}
 .meta{color:#666;font-size:.95rem}
 .spacer{flex:1}
 button{background:#111;color:#fff;border:0;border-radius:8px;padding:.6rem 1.25rem;font-size:1rem;cursor:pointer;white-space:nowrap}
 button:disabled{opacity:.5;cursor:default}
 .url{font-family:monospace;font-size:.95rem;word-break:break-all}
 #out{display:flex;align-items:center;gap:.75rem}
 a{color:#2563eb}
 main{flex:1;min-height:0;display:flex}
 main iframe{flex:1;width:100%;border:0}
 .imgwrap{flex:1;overflow:auto;display:flex;align-items:center;justify-content:center}
 .imgwrap img{max-width:100%;max-height:100%}
 .fallback{margin:auto}
 @media(prefers-color-scheme:dark){
  body{background:#0d1117;color:#e6edf3}
  header{background:#161b22;border-color:#30363d}
  .meta{color:#8b949e}
  button{background:#e6edf3;color:#0d1117}
  a{color:#4493f8}
 }
</style>
</head>
<body>
<header>
 <span class="name">{{.Name}}</span>
 <span class="meta" id="status">Private · in memory on this machine</span>
 <span class="spacer"></span>
 <span id="out"></span>
 <button id="pub" data-id="{{.ID}}">Make public</button>
</header>
<main>
 {{if .IsImage}}<div class="imgwrap"><img src="{{.RawURL}}" alt="{{.Name}}"></div>
 {{else if .IsFrame}}<iframe src="{{.RawURL}}" title="{{.Name}}"></iframe>
 {{else}}<a class="fallback" href="{{.RawURL}}" download="{{.Name}}">Download {{.Name}}</a>{{end}}
</main>
<script>
const btn=document.getElementById('pub'),out=document.getElementById('out'),status=document.getElementById('status');
btn.addEventListener('click',async()=>{
 btn.disabled=true;btn.textContent='Publishing…';
 try{
  const r=await fetch('/api/v1/uploads/publish',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({id:btn.dataset.id})});
  if(!r.ok){throw new Error((await r.text())||('HTTP '+r.status))}
  const d=await r.json();
  status.textContent='Public · expires in 60 days';
  const link=document.createElement('a');link.href=d.url;link.textContent=d.url;
  const wrap=document.createElement('span');wrap.className='url';wrap.append(link);
  const copy=document.createElement('button');copy.textContent='Copy link';
  copy.addEventListener('click',async()=>{
   await navigator.clipboard.writeText(d.url);
   copy.textContent='Copied ✓';setTimeout(()=>{copy.textContent='Copy link'},1500);
  });
  out.replaceChildren(wrap,copy);
  btn.remove();
 }catch(e){btn.disabled=false;btn.textContent='Make public';out.innerHTML='<span class="meta" style="color:#b00">'+e.message+'</span>'}
});
</script>
</body>
</html>`))
