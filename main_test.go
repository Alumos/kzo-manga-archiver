package main

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePage(t *testing.T) {
	got, err := parsePage([]byte(`<title>测试漫画</title><a href="/manga/a">漫画</a><a href="/chapter/2">第二话</a><img data-src="/images/1.jpg"><img src="/images/1.jpg">`), "https://kzo.moe/")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "测试漫画" || len(got.Links) != 2 || len(got.Images) != 1 {
		t.Fatalf("unexpected parsed page: %+v", got)
	}
}

func TestSafeName(t *testing.T) {
	if got := safeName(`../漫画:/一`); got != "_漫画__一" {
		t.Fatalf("safeName = %q", got)
	}
}

func TestChapterOutput(t *testing.T) {
	dir, filename := chapterOutput(Config{NASDir: "/downloads"}, "愛情", "木頭風紀委員和迷你裙JK的故事", "卷 01")
	wantDir := filepath.Join("/downloads", "愛情", "木頭風紀委員和迷你裙JK的故事")
	if dir != wantDir || filename != "[Kmoe][木頭風紀委員和迷你裙JK的故事]卷01.epub" {
		t.Fatalf("chapter output = %q, %q", dir, filename)
	}
}

func TestDownloadedChapterMarker(t *testing.T) {
	cfg := Config{NASDir: t.TempDir()}
	dir, filename := chapterOutput(cfg, "愛情", "測試漫畫", "卷 01")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), []byte("epub"), 0o600); err != nil {
		t.Fatal(err)
	}
	chapters, err := annotateDownloaded(cfg, "愛情", "測試漫畫", []Chapter{{Title: "卷 01", Order: 1}})
	if err != nil || len(chapters) != 1 || !chapters[0].Downloaded || chapters[0].FileName != filename {
		t.Fatalf("download marker = %+v, err = %v", chapters, err)
	}
}

func TestSaveFileCleansTempOnError(t *testing.T) {
	dir := t.TempDir()
	if _, err := saveFile(dir, "book.epub", func(*os.File) error { return io.ErrUnexpectedEOF }); err == nil {
		t.Fatal("saveFile should return the write error")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary files remain: %+v", entries)
	}
}

func TestSummarizeJobTracksChapterStates(t *testing.T) {
	job := newJob("1", downloadRequest{ComicName: "測試漫畫", Chapters: []Chapter{{Title: "卷 01"}, {Title: "卷 02"}}})
	job.Chapters[0].Status = "completed"
	job.Chapters[0].File = "/downloads/卷01.epub"
	job.Chapters[1].Status = "failed"
	job.Chapters[1].Error = "upstream error"
	summarizeJob(job)
	if job.Status != "completed_with_errors" || job.Done != 2 || len(job.Files) != 1 || len(job.Failures) != 1 {
		t.Fatalf("job summary = %+v", job)
	}
	job.Chapters[1].Status = "queued"
	job.Chapters[1].Error = ""
	summarizeJob(job)
	if job.Status != "queued" || job.Done != 1 {
		t.Fatalf("queued retry summary = %+v", job)
	}
}

func TestBookofVolumeCovers(t *testing.T) {
	if got, ok := bookofURLForKzoComic("https://kzo.moe/c/11842.htm"); !ok || got != "https://bookof.moe/b/11842.htm" {
		t.Fatalf("bookof URL = %q, %v", got, ok)
	}
	chapters := []Chapter{{Title: "卷 01", Order: 1}, {Title: "卷 02", Order: 2}, {Title: "卷 03", Order: 3}}
	data := []byte(`
<script>parent.postMessage("datainfo-V=1,卷,卷 01,0,https://kmimg.mxomo.com/cover/one.jpg?sign=1,https://kmimg.mxomo.com/cover/one-full.jpg?sign=1,*");</script>
<script>parent.postMessage("datainfo-V=2,卷,卷 02,0,,https://kmimg.mxomo.com/cover/two-full.jpg?sign=2,*");</script>`)
	got := applyBookofCovers(Config{UpstreamURL: "https://kzo.moe"}, data, chapters)
	if got[0].CoverURL == "" || got[1].CoverURL != "" || got[2].CoverURL != "" {
		t.Fatalf("volume covers = %+v", got)
	}
	coverURL, _ := url.Parse("https://kmimg.mxomo.com/cover/sigl/one.jpg")
	if !allowedCoverHost(coverURL, "https://kzo.moe") || coverReferer(coverURL, "https://kzo.moe") != "https://bookof.moe/" {
		t.Fatalf("bookof cover proxy rules rejected the CDN URL")
	}
}

func TestProxyConfiguration(t *testing.T) {
	proxyURL, err := parseProxyURL("http://192.168.31.3:7890")
	if err != nil || proxyURL.Host != "192.168.31.3:7890" {
		t.Fatalf("proxy URL = %v, %v", proxyURL, err)
	}
	if _, err := parseProxyURL("socks5://127.0.0.1:1080"); err == nil {
		t.Fatal("socks5 proxy should be rejected without a SOCKS dependency")
	}
	client := buildHTTPClient(Config{ProxyURL: "http://192.168.31.3:7890", ProxyEnabled: true}, nil)
	request := httptest.NewRequest(http.MethodGet, "https://www.google.com", nil)
	actual, err := client.Transport.(*http.Transport).Proxy(request)
	if err != nil || actual == nil || actual.Host != proxyURL.Host {
		t.Fatalf("enabled proxy = %v, %v", actual, err)
	}
	direct := buildHTTPClient(Config{ProxyURL: "http://192.168.31.3:7890", ProxyEnabled: false}, nil)
	actual, err = direct.Transport.(*http.Transport).Proxy(request)
	if err != nil || actual != nil {
		t.Fatalf("disabled proxy = %v, %v", actual, err)
	}
}

func TestKzoCardParser(t *testing.T) {
	got, err := parsePage([]byte(`<a id="sel_type_all" href="/l/all/">全部</a><script>disp_divpage( "input_go", "", "946", false, true, false, true, "01", "02", "03", "04", "05", "https://kzo.moe/l/--/1.htm", "https://kzo.moe/l/--/11.htm", "https://kzo.moe/l/--/0.htm", "https://kzo.moe/l/--/2.htm", "https://kzo.moe/l/--/1.htm", "https://kzo.moe/l/--/2.htm", "https://kzo.moe/l/--/3.htm", "https://kzo.moe/l/--/4.htm", "https://kzo.moe/l/--/5.htm", "--" );</script><script>disp_divinfo( "div_info_"+"1", "/c/abc.htm", "cover.jpg", "#fff", "none", "none", "none", "none", "9.1", "測試漫畫", "作者", "連載", "" );</script>`), "https://kzo.moe/")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.KzoCards) != 1 || got.KzoCards[0].Title != "測試漫畫" || got.KzoCards[0].URL != "https://kzo.moe/c/abc.htm" || got.KzoCards[0].CoverURL != "https://kzo.moe/cover.jpg" {
		t.Fatalf("unexpected kzo card: %+v", got.KzoCards)
	}
	if got.Total != 946 || len(got.Categories) != 1 || len(got.PageLinks) != 7 {
		t.Fatalf("unexpected kzo metadata: total=%d categories=%v pages=%v", got.Total, got.Categories, got.PageLinks)
	}
}

func TestKzoDetailParser(t *testing.T) {
	body := []byte(`<img class="img_book" src="/cover.jpg"><font class="text_bglight_big">测试漫画</font><font class="text_bglight">别名作品</font><br><font>作者：<a>作者甲</a></font><font>狀態：連載　地區：日本　語言：繁體　最後出版：2026　更新：07-27</font><font>版本：SE　掃者：漢化組</font><font>訂閱：12　收藏：34　讀過：5　熱度：678</font><font>分類：<font>愛情<font>(30)</font></font>　治癒<font>(9)</font></font>&nbsp;<table class="book_score"><font>9.1</font>383人評價</table><div id="div_desc_content">这是简介<br>第二行</div>`)
	detail, err := parseComicDetail(body, "https://kzo.moe/c/test.htm")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Title != "测试漫画" || detail.Author != "作者甲" || detail.Score != 9.1 || detail.ScoreCount != 383 || detail.CoverURL != "https://kzo.moe/cover.jpg" || detail.Description != "这是简介 第二行" {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestLoginScanAndEPUB(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" && r.Method == http.MethodGet {
			http.SetCookie(w, &http.Cookie{Name: "seed", Value: "ok", Path: "/"})
			io.WriteString(w, `<form action="/login" method="post"><input name="email"><input type="password" name="passwd"></form>`)
			return
		}
		if r.URL.Path == "/login" && r.Method == http.MethodPost {
			_ = r.ParseForm()
			if r.Form.Get("email") == "test@example.com" && r.Form.Get("passwd") == "secret" && hasCookie(r, "seed", "ok") {
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "ok", Path: "/"})
				io.WriteString(w, "logged in")
			} else {
				w.WriteHeader(http.StatusUnauthorized)
			}
			return
		}
		if r.URL.Path == "/" {
			if !hasCookie(r, "session", "ok") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `<a href="/c/demo">Demo 漫画</a>`)
			return
		}
		if r.URL.Path == "/c/demo" {
			io.WriteString(w, `<a href="/chapter/2">第二话</a><a href="/chapter/1">第一话</a>`)
			return
		}
		if r.URL.Path == "/chapter/1" {
			io.WriteString(w, `<img src="/image/1.jpg">`)
			return
		}
		if r.URL.Path == "/chapter/2" {
			io.WriteString(w, `<img src="/image/2.jpg">`)
			return
		}
		if r.URL.Path == "/image/1.jpg" || r.URL.Path == "/image/2.jpg" {
			if r.Header.Get("Referer") == "" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "image/jpeg")
			io.WriteString(w, "fake image")
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	app := newApp()
	app.mu.Lock()
	app.cfg.UpstreamURL = server.URL
	app.cfg.LoginPath = "/login"
	app.cfg.LibraryPath = "/"
	app.cfg.NASDir = t.TempDir()
	app.cfg.SessionFile = filepath.Join(t.TempDir(), "session.json")
	app.cfg.Workers = 2
	app.mu.Unlock()
	if err := app.login(context.Background(), "test@example.com", "secret"); err != nil {
		t.Fatal(err)
	}
	library, err := app.scanLibraryPage(context.Background(), 1, "", "")
	if err != nil || len(library.Comics) != 1 || library.Comics[0].Title != "Demo 漫画" {
		t.Fatalf("library = %+v, err = %v", library.Comics, err)
	}
	chapters, err := app.scanChapters(context.Background(), library.Comics[0].URL)
	if err != nil || len(chapters) != 2 || chapters[0].Order != 1 {
		t.Fatalf("chapters = %+v, err = %v", chapters, err)
	}
	filename, err := app.downloadChapter(context.Background(), app.config(), library.Comics[0].Title, "", library.Comics[0].URL, chapters[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	book, err := zip.OpenReader(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer book.Close()
	if len(book.File) < 5 {
		t.Fatalf("EPUB has too few files: %d", len(book.File))
	}
}

func TestAnonymousLibraryHandler(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			io.WriteString(w, `<a href="/c/public">公开漫画</a>`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	app := newApp()
	app.mu.Lock()
	app.cfg.UpstreamURL = server.URL
	app.cfg.LibraryPath = "/"
	app.cfg.Authenticated = false
	app.mu.Unlock()
	record := httptest.NewRecorder()
	app.libraryHandler(record, httptest.NewRequest(http.MethodGet, "/api/library", nil))
	if record.Code != http.StatusOK || !strings.Contains(record.Body.String(), "公开漫画") {
		t.Fatalf("anonymous library = %d %s", record.Code, record.Body.String())
	}
}

func TestSessionPersistence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" && r.Method == http.MethodGet {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "ok", Path: "/"})
			io.WriteString(w, `<form action="/login" method="post"><input name="email"><input name="passwd"></form>`)
			return
		}
		if r.URL.Path == "/login" && r.Method == http.MethodPost {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "ok", Path: "/"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	sessionFile := filepath.Join(t.TempDir(), "session.json")
	app := newApp()
	app.mu.Lock()
	app.cfg.UpstreamURL = server.URL
	app.cfg.LoginPath = "/login"
	app.cfg.SessionFile = sessionFile
	app.mu.Unlock()
	if err := app.login(context.Background(), "test@example.com", "secret"); err != nil {
		t.Fatal(err)
	}

	restored := newApp()
	restored.mu.Lock()
	restored.cfg.UpstreamURL = server.URL
	restored.cfg.SessionFile = sessionFile
	restored.mu.Unlock()
	restored.loadSession()
	if !restored.authenticated() || restored.config().Username != "test@example.com" {
		t.Fatalf("session was not restored: %+v", restored.config())
	}
}

func TestKzoDownloadFallback(t *testing.T) {
	var epub bytes.Buffer
	archive := zip.NewWriter(&epub)
	entry, err := archive.Create("mimetype")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(entry, "application/epub+zip"); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/getdownurl.php":
			if r.URL.Query().Get("vip") == "1" {
				io.WriteString(w, `{"url":"`+server.URL+`/working.epub"}`)
			} else {
				io.WriteString(w, `{"url":"`+server.URL+`/broken.epub"}`)
			}
		case "/broken.epub":
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `<title>Worker threw exception</title>`)
		case "/working.epub":
			w.Write(epub.Bytes())
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	app := newApp()
	app.mu.Lock()
	app.cfg.UpstreamURL = server.URL
	app.cfg.NASDir = t.TempDir()
	app.mu.Unlock()
	progressCalls := 0
	filename, err := app.downloadKzoEPUB(context.Background(), app.config(), "测试漫画", "", Chapter{Title: "卷 02", Order: 2, DownloadURL: server.URL + "/getdownurl.php?vip=0"}, func(done, total int64) {
		progressCalls++
	})
	if err != nil {
		t.Fatal(err)
	}
	if progressCalls == 0 {
		t.Fatal("download did not report progress")
	}
	if _, err := zip.OpenReader(filename); err != nil {
		t.Fatal(err)
	}
}

func hasCookie(r *http.Request, name, value string) bool {
	cookie, err := r.Cookie(name)
	return err == nil && cookie.Value == value
}

func TestCoverProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		io.WriteString(w, "cover")
	}))
	defer upstream.Close()
	app := newApp()
	app.mu.Lock()
	app.cfg.UpstreamURL = upstream.URL
	app.mu.Unlock()
	record := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/cover?url="+url.QueryEscape(upstream.URL+"/cover.jpg"), nil)
	app.coverHandler(record, request)
	if record.Code != http.StatusOK || record.Body.String() != "cover" || !strings.HasPrefix(record.Header().Get("Content-Type"), "image/png") {
		t.Fatalf("cover proxy = %d %q %q", record.Code, record.Body.String(), record.Header().Get("Content-Type"))
	}
}

func TestNASFileManager(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "old.epub"), []byte("epub"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "taken.epub"), []byte("taken"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := newApp()
	app.mu.Lock()
	app.cfg.NASDir = root
	app.mu.Unlock()

	list := httptest.NewRecorder()
	app.filesHandler(list, httptest.NewRequest(http.MethodGet, "/api/files", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "old.epub") {
		t.Fatalf("file list = %d %s", list.Code, list.Body.String())
	}

	rename := httptest.NewRecorder()
	app.renameFileHandler(rename, httptest.NewRequest(http.MethodPost, "/api/files/rename", strings.NewReader(`{"path":"old.epub","newName":"new.epub"}`)))
	if rename.Code != http.StatusOK {
		t.Fatalf("rename = %d %s", rename.Code, rename.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "new.epub")); err != nil {
		t.Fatal(err)
	}

	traversal := httptest.NewRecorder()
	app.filesHandler(traversal, httptest.NewRequest(http.MethodGet, "/api/files?path=../secret", nil))
	if traversal.Code != http.StatusBadRequest {
		t.Fatalf("traversal status = %d", traversal.Code)
	}
	rootRename := httptest.NewRecorder()
	app.renameFileHandler(rootRename, httptest.NewRequest(http.MethodPost, "/api/files/rename", strings.NewReader(`{"path":"missing/..","newName":"renamed"}`)))
	if rootRename.Code != http.StatusBadRequest {
		t.Fatalf("root rename = %d", rootRename.Code)
	}

	conflict := httptest.NewRecorder()
	app.renameFileHandler(conflict, httptest.NewRequest(http.MethodPost, "/api/files/rename", strings.NewReader(`{"path":"new.epub","newName":"taken.epub"}`)))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("rename conflict = %d", conflict.Code)
	}

	remove := httptest.NewRecorder()
	app.filesHandler(remove, httptest.NewRequest(http.MethodDelete, "/api/files?path=new.epub", nil))
	if remove.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", remove.Code, remove.Body.String())
	}
}
