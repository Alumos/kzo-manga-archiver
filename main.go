package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web
var frontend embed.FS

var (
	anchorRE           = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a>`)
	imageRE            = regexp.MustCompile(`(?is)<img\b([^>]*)>`)
	formRE             = regexp.MustCompile(`(?is)<form\b([^>]*)>(.*?)</form>`)
	inputRE            = regexp.MustCompile(`(?is)<input\b([^>]*)>`)
	kzoCardRE          = regexp.MustCompile(`(?is)disp_divinfo\s*\((.*?)\)\s*;`)
	jsStringRE         = regexp.MustCompile(`"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'`)
	kzoBookIDRE        = regexp.MustCompile(`(?i)\bvar\s+bookid\s*=\s*["']([^"']+)["']`)
	kzoBookDataRE      = regexp.MustCompile(`(?i)(/book_data\.php\?h=[^"']+)`)
	kzoVolInfoRE       = regexp.MustCompile(`(?s)volinfo=([^"']*)`)
	kzoDetailTitleRE   = regexp.MustCompile(`(?is)<font[^>]*class=["'][^"']*text_bglight_big[^"']*["'][^>]*>(.*?)</font>`)
	kzoDetailAliasRE   = regexp.MustCompile(`(?is)<font[^>]*class=["'][^"']*text_bglight[^"']*["'][^>]*>(.*?)</font>\s*<br`)
	kzoDetailAuthorRE  = regexp.MustCompile(`(?is)作者：.*?<a[^>]*>(.*?)</a>`)
	kzoDetailMetaRE    = regexp.MustCompile(`(?is)狀態：\s*([^　<\s]+).*?地區：\s*([^　<\s]+).*?語言：\s*([^　<\s]+).*?最後出版：\s*([^　<\s]+).*?更新：\s*([^　<\s]+)`)
	kzoDetailVersionRE = regexp.MustCompile(`(?is)版本：\s*([^　<\s]+).*?掃者：\s*([^　<\s]+)`)
	kzoDetailStatsRE   = regexp.MustCompile(`(?is)訂閱：\s*([0-9]+).*?收藏：\s*([0-9]+).*?讀過：\s*([0-9]+).*?熱度：\s*([0-9]+)`)
	kzoDetailTagsRE    = regexp.MustCompile(`(?is)<font[^>]*class=["'][^"']*text_bglight[^"']*["'][^>]*>\s*分類：(.*?)</font>\s*&nbsp;`)
	kzoDetailScoreRE   = regexp.MustCompile(`(?is)<table[^>]*class=["'][^"']*book_score[^"']*["'][^>]*>.*?<font[^>]*>([0-9]+(?:\.[0-9]+)?)</font>.*?([0-9]+)人評價`)
	kzoDetailDescRE    = regexp.MustCompile(`(?is)<div[^>]*id=["']div_desc_content["'][^>]*>(.*?)</div>`)
	kzoDetailDescJSRE  = regexp.MustCompile(`(?is)document\.getElementById\(["']div_desc_content["']\)\.innerHTML\s*=\s*"((?:\\.|[^"\\])*)"`)
	kzoComicPathRE     = regexp.MustCompile(`^/c/([0-9]+)\.htm$`)
	bookofVolumeDataRE = regexp.MustCompile(`(?i)(?:https?://bookof\.moe)?/data_vol\.php\?h=[^"']+`)
	bookofVolumeRE     = regexp.MustCompile(`(?i)datainfo-V=([0-9]+),[^,]*,([^,]*),[^,]*,([^,]*),([^",]*)`)
	kzoListPageRE      = regexp.MustCompile(`^/l/[^/]+/[0-9]+\.htm$`)
	kzoPageRE          = regexp.MustCompile(`(?is)disp_divpage\s*\((.*?)\)\s*;`)
	kzoTotalRE         = regexp.MustCompile(`(?is)disp_divpage\s*\(\s*[^,]+,\s*[^,]+,\s*["']?(\d+)`)
	titleRE            = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title>`)
	h1RE               = regexp.MustCompile(`(?is)<h1\b[^>]*>(.*?)</h1>`)
	tagRE              = regexp.MustCompile(`(?is)<[^>]+>`)
	attrRE             = regexp.MustCompile(`(?i)([a-z_:][-a-z0-9_:]*)\s*=\s*["']([^"']*)["']`)
	numberRE           = regexp.MustCompile(`(?i)(?:chapter|episode|ep|话|集|卷|vol(?:ume)?)?\s*([0-9]+(?:\.[0-9]+)?)`)
)

type Config struct {
	UpstreamURL   string `json:"upstreamUrl"`
	LoginPath     string `json:"loginPath"`
	LibraryPath   string `json:"libraryPath"`
	NASDir        string `json:"nasDir"`
	ProxyURL      string `json:"proxyUrl"`
	ProxyEnabled  bool   `json:"proxyEnabled"`
	SessionFile   string `json:"-"`
	Username      string `json:"username"`
	Workers       int    `json:"workers"`
	Authenticated bool   `json:"authenticated"`
}

type savedCookie struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Path     string    `json:"path,omitempty"`
	Expires  time.Time `json:"expires,omitempty"`
	Secure   bool      `json:"secure,omitempty"`
	HttpOnly bool      `json:"httpOnly,omitempty"`
}

type savedSession struct {
	UpstreamURL string        `json:"upstreamUrl"`
	Username    string        `json:"username"`
	Cookies     []savedCookie `json:"cookies"`
}

type Comic struct {
	Title    string `json:"title"`
	URL      string `json:"url"`
	CoverURL string `json:"coverUrl,omitempty"`
	Author   string `json:"author,omitempty"`
	Status   string `json:"status,omitempty"`
}

type ComicDetail struct {
	Comic
	Aliases       string   `json:"aliases,omitempty"`
	Description   string   `json:"description,omitempty"`
	Score         float64  `json:"score,omitempty"`
	ScoreCount    int      `json:"scoreCount,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	Region        string   `json:"region,omitempty"`
	Language      string   `json:"language,omitempty"`
	LastPublished string   `json:"lastPublished,omitempty"`
	Updated       string   `json:"updated,omitempty"`
	Version       string   `json:"version,omitempty"`
	ScannedBy     string   `json:"scannedBy,omitempty"`
	Subscribers   int      `json:"subscribers,omitempty"`
	Favorites     int      `json:"favorites,omitempty"`
	ReadCount     int      `json:"readCount,omitempty"`
	Heat          int      `json:"heat,omitempty"`
}

type KzoCategory struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Chapter struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Order       int    `json:"order"`
	DownloadURL string `json:"downloadUrl,omitempty"`
	CoverURL    string `json:"coverUrl,omitempty"`
	FileName    string `json:"fileName,omitempty"`
	Downloaded  bool   `json:"downloaded,omitempty"`
}

type link struct {
	Title    string
	URL      string
	CoverURL string
	Author   string
	Status   string
}

type imageRef struct {
	URL string
}

type page struct {
	Title      string
	Links      []link
	Images     []imageRef
	KzoCards   []link
	Categories []KzoCategory
	PageLinks  []string
	Total      int
}

type libraryResult struct {
	Comics     []Comic       `json:"comics"`
	Categories []KzoCategory `json:"categories,omitempty"`
	Page       int           `json:"page"`
	PageSize   int           `json:"pageSize"`
	Total      int           `json:"total"`
	TotalPages int           `json:"totalPages"`
}

type Job struct {
	ID         string       `json:"id"`
	Status     string       `json:"status"`
	Comic      string       `json:"comic"`
	CoverURL   string       `json:"coverUrl,omitempty"`
	Done       int          `json:"done"`
	Total      int          `json:"total"`
	Files      []string     `json:"files"`
	Failures   []string     `json:"failures"`
	Error      string       `json:"error,omitempty"`
	BytesDone  int64        `json:"bytesDone"`
	BytesTotal int64        `json:"bytesTotal"`
	SpeedBPS   int64        `json:"speedBps"`
	StartedAt  time.Time    `json:"startedAt"`
	FinishedAt time.Time    `json:"finishedAt,omitempty"`
	Chapters   []JobChapter `json:"chapters"`
	request    downloadRequest
}

type JobChapter struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Order      int       `json:"order"`
	Status     string    `json:"status"`
	CoverURL   string    `json:"coverUrl,omitempty"`
	Done       int64     `json:"done"`
	Total      int64     `json:"total"`
	SpeedBPS   int64     `json:"speedBps"`
	File       string    `json:"file,omitempty"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	chapter    Chapter
}

type FileEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	IsDir   bool      `json:"isDir"`
	Symlink bool      `json:"symlink,omitempty"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

type App struct {
	mu            sync.RWMutex
	cfg           Config
	jobs          map[string]*Job
	client        *http.Client
	downloadQueue chan downloadTask
	queueMu       sync.Mutex
	fileMu        sync.Mutex
}

type downloadTask struct {
	JobID     string
	ChapterID string
	Request   downloadRequest
	Chapter   Chapter
}

type downloadRequest struct {
	ComicName string    `json:"comicName"`
	ComicURL  string    `json:"comicUrl"`
	CoverURL  string    `json:"coverUrl,omitempty"`
	Category  string    `json:"category,omitempty"`
	Chapters  []Chapter `json:"chapters"`
}

type settingsRequest struct {
	UpstreamURL  string  `json:"upstreamUrl"`
	LoginPath    string  `json:"loginPath"`
	LibraryPath  string  `json:"libraryPath"`
	NASDir       string  `json:"nasDir"`
	ProxyURL     *string `json:"proxyUrl"`
	ProxyEnabled *bool   `json:"proxyEnabled"`
	Username     string  `json:"username"`
	Password     string  `json:"password"`
	Workers      int     `json:"workers"`
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginForm struct {
	Action        string
	Method        string
	UsernameField string
	PasswordField string
	Values        url.Values
}

func main() {
	app := newApp()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/library", app.libraryHandler)
	mux.HandleFunc("/api/comic", app.comicHandler)
	mux.HandleFunc("/api/cover", app.coverHandler)
	mux.HandleFunc("/api/chapters", app.chaptersHandler)
	mux.HandleFunc("/api/downloaded", app.downloadedHandler)
	mux.HandleFunc("/api/download", app.downloadHandler)
	mux.HandleFunc("/api/jobs", app.jobsHandler)
	mux.HandleFunc("/api/jobs/retry", app.retryJobHandler)
	mux.HandleFunc("/api/proxy/test", app.proxyTestHandler)
	mux.HandleFunc("/api/files", app.filesHandler)
	mux.HandleFunc("/api/files/rename", app.renameFileHandler)
	mux.HandleFunc("/api/settings", app.settingsHandler)
	mux.HandleFunc("/api/auth/login", app.loginHandler)
	mux.HandleFunc("/api/auth/status", app.authStatusHandler)
	mux.HandleFunc("/api/auth/logout", app.logoutHandler)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	staticFS, _ := fs.Sub(frontend, "web")
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin.html", http.StatusFound)
	})
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	addr := env("ADDR", ":8080")
	log.Printf("koxmoe-transfer listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, logging(mux)))
}

func newApp() *App {
	workers, _ := strconv.Atoi(env("WORKERS", "4"))
	if workers < 1 {
		workers = 1
	}
	jar, _ := cookiejar.New(nil)
	downloadDir := env("DOWNLOAD_DIR", env("NAS_DIR", "./downloads"))
	proxyEnabled, _ := strconv.ParseBool(env("PROXY_ENABLED", "false"))
	app := &App{
		cfg: Config{
			UpstreamURL:  strings.TrimRight(env("UPSTREAM_URL", "https://kzo.moe"), "/"),
			LoginPath:    env("LOGIN_PATH", "/login.php"),
			LibraryPath:  env("LIBRARY_PATH", "/"),
			NASDir:       downloadDir,
			ProxyURL:     strings.TrimSpace(os.Getenv("PROXY_URL")),
			ProxyEnabled: proxyEnabled,
			SessionFile:  env("SESSION_FILE", ".kzo-session.json"),
			Username:     os.Getenv("KZO_USERNAME"),
			Workers:      workers,
		},
		jobs:   make(map[string]*Job),
		client: buildHTTPClient(Config{ProxyURL: strings.TrimSpace(os.Getenv("PROXY_URL")), ProxyEnabled: proxyEnabled}, jar),
		// ponytail: in-memory FIFO; persist jobs only when restart-resume is required.
		downloadQueue: make(chan downloadTask, 4096),
	}
	app.loadSession()
	for i := 0; i < 3; i++ {
		go app.downloadWorker()
	}
	return app
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func buildHTTPClient(cfg Config, jar http.CookieJar) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.ProxyEnabled && strings.TrimSpace(cfg.ProxyURL) != "" {
		proxyURL, err := parseProxyURL(cfg.ProxyURL)
		if err != nil {
			log.Printf("invalid proxy configuration, using direct connection: %v", err)
		} else {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	}
	return &http.Client{Timeout: 15 * time.Minute, Jar: jar, Transport: transport}
}

func parseProxyURL(raw string) (*url.URL, error) {
	u, err := validHTTPURL(raw)
	if err != nil {
		return nil, errors.New("proxy must be an http or https URL")
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("proxy URL must not contain a path or query")
	}
	return u, nil
}

func (a *App) config() Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg
}

func (a *App) authenticated() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg.Authenticated
}

func (a *App) httpClient() *http.Client {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.client
}

func (a *App) resetSession() {
	jar, _ := cookiejar.New(nil)
	cfg := a.config()
	a.mu.Lock()
	a.client = buildHTTPClient(cfg, jar)
	a.cfg.Authenticated = false
	a.mu.Unlock()
	_ = os.Remove(cfg.SessionFile)
}

func (a *App) loadSession() {
	cfg := a.config()
	upstream, err := validHTTPURL(cfg.UpstreamURL)
	if err != nil || cfg.SessionFile == "" {
		return
	}
	data, err := os.ReadFile(cfg.SessionFile)
	if err != nil {
		return
	}
	var saved savedSession
	if json.Unmarshal(data, &saved) != nil || (saved.UpstreamURL != "" && strings.TrimRight(saved.UpstreamURL, "/") != cfg.UpstreamURL) || len(saved.Cookies) == 0 {
		return
	}
	cookies := make([]*http.Cookie, 0, len(saved.Cookies))
	for _, item := range saved.Cookies {
		if item.Name == "" {
			continue
		}
		cookies = append(cookies, &http.Cookie{Name: item.Name, Value: item.Value, Path: item.Path, Expires: item.Expires, Secure: item.Secure, HttpOnly: item.HttpOnly})
	}
	client := a.httpClient()
	client.Jar.SetCookies(upstream, cookies)
	if len(client.Jar.Cookies(upstream)) > 0 {
		a.mu.Lock()
		a.cfg.Username = saved.Username
		a.cfg.Authenticated = true
		a.mu.Unlock()
	}
}

func (a *App) saveSession(username string) error {
	cfg := a.config()
	upstream, err := validHTTPURL(cfg.UpstreamURL)
	if err != nil || cfg.SessionFile == "" {
		return errors.New("invalid session storage configuration")
	}
	saved := savedSession{UpstreamURL: cfg.UpstreamURL, Username: username}
	for _, cookie := range a.httpClient().Jar.Cookies(upstream) {
		saved.Cookies = append(saved.Cookies, savedCookie{Name: cookie.Name, Value: cookie.Value, Path: cookie.Path, Expires: cookie.Expires, Secure: cookie.Secure, HttpOnly: cookie.HttpOnly})
	}
	if len(saved.Cookies) == 0 {
		return errors.New("login succeeded without cookies to save")
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(cfg.SessionFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".kzo-session-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, cfg.SessionFile)
}

func (a *App) login(ctx context.Context, username, password string) error {
	if isKzoURL(a.config().UpstreamURL) {
		return a.loginKzo(ctx, username, password)
	}
	return a.loginGeneric(ctx, username, password)
}

func (a *App) loginKzo(ctx context.Context, username, password string) error {
	a.resetSession()
	cfg := a.config()
	loginURL := resolveURL(cfg.UpstreamURL, cfg.LoginPath)
	upstream, err := validHTTPURL(cfg.UpstreamURL)
	if err != nil || loginURL == "" {
		return errors.New("invalid upstream or login path")
	}
	client := a.httpClient()
	getRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, loginURL, nil)
	if err != nil {
		return err
	}
	getRequest.Header.Set("User-Agent", "koxmoe-transfer/0.1")
	response, err := client.Do(getRequest)
	if err != nil {
		return err
	}
	_, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	response.Body.Close()
	if readErr != nil {
		return readErr
	}
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return fmt.Errorf("login page returned %s", response.Status)
	}

	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	if err := writer.WriteField("email", username); err != nil {
		return err
	}
	if err := writer.WriteField("passwd", password); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	loginAction := resolveURL(cfg.UpstreamURL, "/login_act.php")
	postRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, loginAction, &payload)
	if err != nil {
		return err
	}
	postRequest.Header.Set("User-Agent", "koxmoe-transfer/0.1")
	postRequest.Header.Set("Content-Type", writer.FormDataContentType())
	postRequest.Header.Set("X-KM-FROM", "KMOE/3.0.0(WEB) POST "+cfg.LoginPath)
	response, err = client.Do(postRequest)
	if err != nil {
		return err
	}
	resultBody, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	response.Body.Close()
	if readErr != nil {
		return readErr
	}
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return fmt.Errorf("login failed: %s", response.Status)
	}
	var result struct {
		Msg   string `json:"msg"`
		MsgID string `json:"msgid"`
	}
	if err := json.Unmarshal(resultBody, &result); err != nil {
		return fmt.Errorf("unexpected login response: %s", strings.TrimSpace(string(resultBody)))
	}
	if result.MsgID != "m100" {
		if result.Msg == "" {
			result.Msg = "kzo.moe rejected the login"
		}
		return errors.New(result.Msg)
	}
	if len(client.Jar.Cookies(upstream)) == 0 {
		return errors.New("login succeeded without a session cookie")
	}
	if err := a.saveSession(username); err != nil {
		return fmt.Errorf("login succeeded but could not save session: %w", err)
	}
	a.setAuthenticated(username)
	return nil
}

func (a *App) setAuthenticated(username string) {
	a.mu.Lock()
	a.cfg.Username = username
	a.cfg.Authenticated = true
	a.mu.Unlock()
}

func (a *App) loginGeneric(ctx context.Context, username, password string) error {
	a.resetSession()
	cfg := a.config()
	loginURL := resolveURL(cfg.UpstreamURL, cfg.LoginPath)
	upstream, err := validHTTPURL(cfg.UpstreamURL)
	if err != nil || loginURL == "" {
		return errors.New("invalid upstream or login path")
	}
	client := a.httpClient()
	getRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, loginURL, nil)
	if err != nil {
		return err
	}
	getRequest.Header.Set("User-Agent", "koxmoe-transfer/0.1")
	response, err := client.Do(getRequest)
	if err != nil {
		return err
	}
	pageBody, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	response.Body.Close()
	if readErr != nil {
		return readErr
	}
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return fmt.Errorf("login page returned %s", response.Status)
	}
	form, err := parseLoginForm(pageBody, loginURL)
	if err != nil {
		return err
	}
	form.Values.Set(form.UsernameField, username)
	form.Values.Set(form.PasswordField, password)

	var submit *http.Request
	if form.Method == http.MethodGet {
		target, _ := url.Parse(form.Action)
		query := target.Query()
		for key, values := range form.Values {
			for _, value := range values {
				query.Add(key, value)
			}
		}
		target.RawQuery = query.Encode()
		submit, err = http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	} else {
		submit, err = http.NewRequestWithContext(ctx, http.MethodPost, form.Action, strings.NewReader(form.Values.Encode()))
	}
	if err != nil {
		return err
	}
	if form.Method == http.MethodPost {
		submit.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	submit.Header.Set("User-Agent", "koxmoe-transfer/0.1")
	submit.Header.Set("Referer", loginURL)
	response, err = client.Do(submit)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return fmt.Errorf("login failed: %s", response.Status)
	}
	if len(client.Jar.Cookies(upstream)) == 0 {
		return errors.New("login did not create a session; check LOGIN_PATH or the site's login form")
	}
	if err := a.saveSession(username); err != nil {
		return fmt.Errorf("login succeeded but could not save session: %w", err)
	}
	a.setAuthenticated(username)
	return nil
}

func (a *App) settingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		cfg := a.config()
		writeJSON(w, http.StatusOK, map[string]any{
			"upstreamUrl":   cfg.UpstreamURL,
			"loginPath":     cfg.LoginPath,
			"libraryPath":   cfg.LibraryPath,
			"nasDir":        cfg.NASDir,
			"proxyUrl":      cfg.ProxyURL,
			"proxyEnabled":  cfg.ProxyEnabled,
			"username":      cfg.Username,
			"workers":       cfg.Workers,
			"authenticated": cfg.Authenticated,
		})
		return
	}
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input settingsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid settings: "+err.Error())
		return
	}
	if input.UpstreamURL != "" {
		if _, err := validHTTPURL(input.UpstreamURL); err != nil {
			writeError(w, http.StatusBadRequest, "invalid upstreamUrl")
			return
		}
	}
	current := a.config()
	proxyURL := current.ProxyURL
	if input.ProxyURL != nil {
		proxyURL = strings.TrimSpace(*input.ProxyURL)
		if proxyURL != "" {
			if _, err := parseProxyURL(proxyURL); err != nil {
				writeError(w, http.StatusBadRequest, "invalid proxyUrl: "+err.Error())
				return
			}
		}
	}
	proxyEnabled := current.ProxyEnabled
	if input.ProxyEnabled != nil {
		proxyEnabled = *input.ProxyEnabled
	}
	if proxyEnabled && proxyURL == "" {
		writeError(w, http.StatusBadRequest, "proxyEnabled requires proxyUrl")
		return
	}
	a.mu.Lock()
	upstreamChanged := input.UpstreamURL != "" && strings.TrimRight(input.UpstreamURL, "/") != a.cfg.UpstreamURL
	proxyChanged := proxyURL != a.cfg.ProxyURL || proxyEnabled != a.cfg.ProxyEnabled
	if input.UpstreamURL != "" {
		a.cfg.UpstreamURL = strings.TrimRight(input.UpstreamURL, "/")
	}
	if input.LoginPath != "" {
		a.cfg.LoginPath = input.LoginPath
	}
	if input.LibraryPath != "" {
		a.cfg.LibraryPath = input.LibraryPath
	}
	if input.NASDir != "" {
		a.cfg.NASDir = input.NASDir
	}
	if input.Workers > 0 && input.Workers <= 32 {
		a.cfg.Workers = input.Workers
	}
	if input.Username != "" {
		a.cfg.Username = input.Username
	}
	if input.ProxyURL != nil {
		a.cfg.ProxyURL = proxyURL
	}
	if input.ProxyEnabled != nil {
		a.cfg.ProxyEnabled = proxyEnabled
	}
	if upstreamChanged {
		a.cfg.Authenticated = false
	}
	if proxyChanged {
		a.client = buildHTTPClient(a.cfg, a.client.Jar)
	}
	a.mu.Unlock()
	if upstreamChanged {
		a.resetSession()
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (a *App) loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input loginRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid login request: "+err.Error())
		return
	}
	if strings.TrimSpace(input.Username) == "" || input.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	if err := a.login(r.Context(), input.Username, input.Password); err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "authenticated", "username": input.Username})
}

func (a *App) authStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	cfg := a.config()
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": cfg.Authenticated, "username": cfg.Username})
}

func (a *App) logoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	a.resetSession()
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func (a *App) libraryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	pageNumber, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if pageNumber < 1 {
		pageNumber = 1
	}
	if pageNumber > 999 {
		writeError(w, http.StatusBadRequest, "page must be between 1 and 999")
		return
	}
	result, err := a.scanLibraryPage(r.Context(), pageNumber, r.URL.Query().Get("search"), r.URL.Query().Get("category"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) comicHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !a.authenticated() {
		writeError(w, http.StatusUnauthorized, "please log in first")
		return
	}
	comicURL := r.URL.Query().Get("url")
	if comicURL == "" || !sameHost(comicURL, a.config().UpstreamURL) {
		writeError(w, http.StatusBadRequest, "url must belong to the configured upstream")
		return
	}
	data, _, err := a.fetch(r.Context(), comicURL, 24<<20)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	detail, err := parseComicDetail(data, comicURL)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (a *App) coverHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	rawURL := r.URL.Query().Get("url")
	u, err := validHTTPURL(rawURL)
	if err != nil || !allowedCoverHost(u, a.config().UpstreamURL) {
		writeError(w, http.StatusBadRequest, "unsupported cover url")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid cover url")
		return
	}
	cfg := a.config()
	req.Header.Set("User-Agent", "koxmoe-transfer/0.1")
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	req.Header.Set("Referer", coverReferer(u, cfg.UpstreamURL))
	response, err := a.httpClient().Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("cover upstream returned %s", response.Status))
		return
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	io.Copy(w, io.LimitReader(response.Body, 20<<20))
}

func allowedCoverHost(u *url.URL, upstream string) bool {
	base, err := validHTTPURL(upstream)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == base.Hostname() || host == "kmimg.moex.ink" || strings.HasSuffix(host, ".moex.ink") || strings.HasSuffix(host, ".mixmoe.com") || strings.HasSuffix(host, ".mxomo.com")
}

func coverReferer(u *url.URL, upstream string) string {
	host := strings.ToLower(u.Hostname())
	if strings.HasSuffix(host, ".moex.ink") || strings.HasSuffix(host, ".mxomo.com") || strings.HasSuffix(host, ".mixmoe.com") {
		return "https://bookof.moe/"
	}
	return strings.TrimRight(upstream, "/") + "/"
}

func (a *App) chaptersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !a.authenticated() {
		writeError(w, http.StatusUnauthorized, "please log in first")
		return
	}
	comicURL := r.URL.Query().Get("url")
	if comicURL == "" {
		writeError(w, http.StatusBadRequest, "missing url")
		return
	}
	if !sameHost(comicURL, a.config().UpstreamURL) {
		writeError(w, http.StatusBadRequest, "url must belong to the configured upstream")
		return
	}
	chapters, err := a.scanChapters(r.Context(), comicURL)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	category := strings.TrimSpace(r.URL.Query().Get("category"))
	if comicName := strings.TrimSpace(r.URL.Query().Get("comicName")); comicName != "" {
		category = a.resolveCategory(r.Context(), comicURL, category)
		chapters, err = annotateDownloaded(a.config(), category, comicName, chapters)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "cannot inspect download directory: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"chapters": chapters, "category": category})
}

func (a *App) downloadedHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !a.authenticated() {
		writeError(w, http.StatusUnauthorized, "please log in first")
		return
	}
	comicName := strings.TrimSpace(r.URL.Query().Get("comicName"))
	if comicName == "" {
		writeError(w, http.StatusBadRequest, "comicName is required")
		return
	}
	comicURL := r.URL.Query().Get("url")
	if comicURL != "" && !sameHost(comicURL, a.config().UpstreamURL) {
		writeError(w, http.StatusBadRequest, "url must belong to the configured upstream")
		return
	}
	category := a.resolveCategory(r.Context(), comicURL, r.URL.Query().Get("category"))
	files, err := downloadedFiles(a.config(), category, comicName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot inspect download directory: "+err.Error())
		return
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	writeJSON(w, http.StatusOK, map[string]any{"files": names})
}

func (a *App) downloadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !a.authenticated() {
		writeError(w, http.StatusUnauthorized, "please log in first")
		return
	}
	var input downloadRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 5<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if strings.TrimSpace(input.ComicName) == "" || input.ComicURL == "" || len(input.Chapters) == 0 {
		writeError(w, http.StatusBadRequest, "comicName, comicUrl and at least one chapter are required")
		return
	}
	if len(input.Chapters) > 2000 {
		writeError(w, http.StatusBadRequest, "too many chapters")
		return
	}
	if _, err := validHTTPURL(input.ComicURL); err != nil {
		writeError(w, http.StatusBadRequest, "invalid comicUrl")
		return
	}
	if !sameHost(input.ComicURL, a.config().UpstreamURL) {
		writeError(w, http.StatusBadRequest, "comicUrl must belong to the configured upstream")
		return
	}
	for i := range input.Chapters {
		if _, err := validHTTPURL(input.Chapters[i].URL); err != nil {
			writeError(w, http.StatusBadRequest, "invalid chapter url")
			return
		}
		if !sameHost(input.Chapters[i].URL, a.config().UpstreamURL) {
			writeError(w, http.StatusBadRequest, "chapter url must belong to the configured upstream")
			return
		}
		if input.Chapters[i].DownloadURL != "" && !sameHost(input.Chapters[i].DownloadURL, a.config().UpstreamURL) {
			writeError(w, http.StatusBadRequest, "download url must belong to the configured upstream")
			return
		}
		if input.Chapters[i].Order < 1 {
			input.Chapters[i].Order = i + 1
		}
	}
	input.Category = a.resolveCategory(r.Context(), input.ComicURL, input.Category)
	files, err := downloadedFiles(a.config(), input.Category, input.ComicName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot inspect download directory: "+err.Error())
		return
	}
	pending := input.Chapters[:0]
	skipped := 0
	for _, chapter := range input.Chapters {
		_, filename := chapterOutput(a.config(), input.Category, input.ComicName, chapter.Title)
		if files[filename] {
			skipped++
			continue
		}
		pending = append(pending, chapter)
	}
	input.Chapters = pending
	if len(input.Chapters) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"status": "skipped", "skipped": skipped, "message": "所选章节都已下载"})
		return
	}

	id := fmt.Sprintf("%d", time.Now().UnixNano())
	job := newJob(id, input)
	a.mu.Lock()
	a.jobs[id] = job
	a.mu.Unlock()
	tasks := make([]downloadTask, len(input.Chapters))
	for index, chapter := range input.Chapters {
		tasks[index] = downloadTask{JobID: id, ChapterID: job.Chapters[index].ID, Request: input, Chapter: chapter}
	}
	a.enqueueTasks(tasks)
	snapshot, _ := a.jobSnapshot(id)
	writeJSON(w, http.StatusAccepted, snapshot)
}

func (a *App) resolveCategory(ctx context.Context, comicURL, category string) string {
	category = strings.TrimSpace(category)
	if category == "" && isKzoURL(a.config().UpstreamURL) && comicURL != "" {
		if body, _, fetchErr := a.fetch(ctx, comicURL, 24<<20); fetchErr == nil {
			if detail, parseErr := parseComicDetail(body, comicURL); parseErr == nil && len(detail.Tags) > 0 {
				category = detail.Tags[0]
			}
		}
	}
	if category == "" {
		return "未分类"
	}
	return category
}

func (a *App) jobsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	a.mu.RLock()
	jobs := make([]Job, 0, len(a.jobs))
	for _, job := range a.jobs {
		copy := *job
		copy.Files = append([]string(nil), job.Files...)
		copy.Failures = append([]string(nil), job.Failures...)
		copy.Chapters = append([]JobChapter(nil), job.Chapters...)
		jobs = append(jobs, copy)
	}
	a.mu.RUnlock()
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].StartedAt.After(jobs[j].StartedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (a *App) retryJobHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !a.authenticated() {
		writeError(w, http.StatusUnauthorized, "please log in first")
		return
	}
	var input struct {
		JobID     string `json:"jobId"`
		ChapterID string `json:"chapterId"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid retry request")
		return
	}
	a.mu.Lock()
	job := a.jobs[input.JobID]
	if job == nil {
		a.mu.Unlock()
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	var chapter *JobChapter
	for index := range job.Chapters {
		if job.Chapters[index].ID == input.ChapterID {
			chapter = &job.Chapters[index]
			break
		}
	}
	if chapter == nil {
		a.mu.Unlock()
		writeError(w, http.StatusNotFound, "chapter not found")
		return
	}
	if chapter.Status != "failed" {
		a.mu.Unlock()
		writeError(w, http.StatusConflict, "only failed chapters can be retried")
		return
	}
	chapter.Status = "queued"
	chapter.Done = 0
	chapter.Total = 0
	chapter.SpeedBPS = 0
	chapter.File = ""
	chapter.Error = ""
	chapter.StartedAt = time.Time{}
	chapter.FinishedAt = time.Time{}
	job.FinishedAt = time.Time{}
	summarizeJob(job)
	task := downloadTask{JobID: job.ID, ChapterID: chapter.ID, Request: job.request, Chapter: chapter.chapter}
	a.mu.Unlock()
	a.enqueueTasks([]downloadTask{task})
	snapshot, _ := a.jobSnapshot(input.JobID)
	writeJSON(w, http.StatusAccepted, snapshot)
}

func (a *App) enqueueTasks(tasks []downloadTask) {
	a.queueMu.Lock()
	defer a.queueMu.Unlock()
	for _, task := range tasks {
		a.downloadQueue <- task
	}
}

func (a *App) proxyTestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	cfg := a.config()
	mode := "直连"
	if cfg.ProxyEnabled {
		mode = "代理"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.google.com/generate_204", nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "mode": mode, "error": err.Error()})
		return
	}
	req.Header.Set("User-Agent", "koxmoe-transfer/0.1")
	started := time.Now()
	response, err := a.httpClient().Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "mode": mode, "elapsedMs": time.Since(started).Milliseconds(), "error": err.Error()})
		return
	}
	defer response.Body.Close()
	io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	ok := response.StatusCode >= 200 && response.StatusCode < 400
	result := map[string]any{"ok": ok, "mode": mode, "status": response.StatusCode, "elapsedMs": time.Since(started).Milliseconds()}
	if !ok {
		result["error"] = response.Status
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) filesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		entries, path, err := a.listFiles(r.URL.Query().Get("path"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"path": path, "entries": entries})
	case http.MethodDelete:
		rawPath := r.URL.Query().Get("path")
		if strings.TrimSpace(rawPath) == "" {
			writeError(w, http.StatusBadRequest, "path is required")
			return
		}
		a.fileMu.Lock()
		defer a.fileMu.Unlock()
		path, _, err := a.nasPath(rawPath)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "file not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if info.Mode()&os.ModeSymlink != 0 {
			writeError(w, http.StatusBadRequest, "symlinks cannot be changed")
			return
		}
		if err := os.Remove(path); err != nil {
			writeError(w, http.StatusConflict, "只能删除空目录或文件："+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *App) renameFileHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Path    string `json:"path"`
		NewName string `json:"newName"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid rename request")
		return
	}
	input.Path = strings.TrimSpace(input.Path)
	input.NewName = strings.TrimSpace(input.NewName)
	if input.Path == "" || input.NewName == "" || input.NewName == "." || input.NewName == ".." || strings.ContainsAny(input.NewName, "/\\") || strings.ContainsRune(input.NewName, 0) {
		writeError(w, http.StatusBadRequest, "invalid file name")
		return
	}
	a.fileMu.Lock()
	defer a.fileMu.Unlock()
	source, rel, err := a.nasPath(input.Path)
	if err != nil || rel == "" {
		if err == nil {
			err = errors.New("cannot rename NAS root")
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if info.Mode()&os.ModeSymlink != 0 {
		writeError(w, http.StatusBadRequest, "symlinks cannot be changed")
		return
	}
	destination := filepath.Join(filepath.Dir(source), input.NewName)
	if _, err := os.Lstat(destination); err == nil {
		writeError(w, http.StatusConflict, "目标名称已存在")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.Rename(source, destination); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "renamed"})
}

func (a *App) listFiles(rawPath string) ([]FileEntry, string, error) {
	path, rel, err := a.nasPath(rawPath)
	if err != nil {
		return nil, "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, "", errors.New("path is not a directory")
	}
	items, err := os.ReadDir(path)
	if err != nil {
		return nil, "", err
	}
	entries := make([]FileEntry, 0, len(items))
	for _, item := range items {
		fullPath := filepath.Join(path, item.Name())
		itemInfo, infoErr := os.Lstat(fullPath)
		if infoErr != nil {
			continue
		}
		itemPath := filepath.ToSlash(filepath.Join(rel, item.Name()))
		if rel == "" {
			itemPath = filepath.ToSlash(item.Name())
		}
		entries = append(entries, FileEntry{Name: item.Name(), Path: itemPath, IsDir: itemInfo.IsDir(), Symlink: itemInfo.Mode()&os.ModeSymlink != 0, Size: itemInfo.Size(), ModTime: itemInfo.ModTime()})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, rel, nil
}

func (a *App) nasPath(rawPath string) (string, string, error) {
	root, err := filepath.Abs(a.config().NASDir)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", "", err
	}
	if rawPath == "" || rawPath == "." {
		return root, "", nil
	}
	if filepath.IsAbs(rawPath) || strings.ContainsRune(rawPath, 0) {
		return "", "", errors.New("path must be relative to NAS root")
	}
	rel := filepath.Clean(filepath.FromSlash(rawPath))
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", errors.New("path escapes NAS root")
	}
	if rel == "." {
		return root, "", nil
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			break
		}
		if statErr != nil {
			return "", "", statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", "", errors.New("symlink paths are not allowed")
		}
	}
	return filepath.Join(root, rel), filepath.ToSlash(rel), nil
}

func (a *App) scanLibraryPage(ctx context.Context, pageNumber int, search, category string) (libraryResult, error) {
	cfg := a.config()
	if isKzoURL(cfg.UpstreamURL) {
		return a.scanKzoLibraryPage(ctx, pageNumber, search, category)
	}
	pageData, err := a.getPage(ctx, resolveURL(cfg.UpstreamURL, cfg.LibraryPath))
	if err != nil {
		return libraryResult{}, err
	}
	links := filterLinks(pageData.Links, cfg.UpstreamURL, cfg.LibraryPath)
	if isKzoURL(cfg.UpstreamURL) && len(pageData.KzoCards) > 0 {
		links = pageData.KzoCards
	}
	if len(links) == 0 {
		return libraryResult{}, errors.New("no comic links found; check LIBRARY_PATH or the site's page structure")
	}
	strong := make([]link, 0, len(links))
	for _, item := range links {
		if hasAny(item.URL, "/comic", "/manga", "/manhua", "/book", "/title", "/c/") {
			strong = append(strong, item)
		}
	}
	if len(strong) > 0 {
		links = strong
	}
	seen := map[string]bool{}
	comics := make([]Comic, 0, len(links))
	for _, item := range links {
		if seen[item.URL] {
			continue
		}
		seen[item.URL] = true
		comics = append(comics, Comic{Title: fallbackTitle(item.Title, item.URL), URL: item.URL, CoverURL: item.CoverURL, Author: item.Author, Status: item.Status})
	}
	sort.SliceStable(comics, func(i, j int) bool { return strings.ToLower(comics[i].Title) < strings.ToLower(comics[j].Title) })
	return libraryResult{Comics: comics, Page: 1, PageSize: len(comics), Total: len(comics), TotalPages: 1}, nil
}

func (a *App) scanKzoLibraryPage(ctx context.Context, pageNumber int, search, category string) (libraryResult, error) {
	cfg := a.config()
	firstURL := kzoLibraryURL(cfg, 1, search, category)
	firstPage, err := a.getPage(ctx, firstURL)
	if err != nil {
		return libraryResult{}, err
	}
	pageURL := kzoPageURL(firstPage, firstURL, pageNumber)
	pageData := firstPage
	if pageURL != firstURL {
		pageData, err = a.getPage(ctx, pageURL)
		if err != nil {
			return libraryResult{}, err
		}
	}
	comics := make([]Comic, 0, len(pageData.KzoCards))
	for _, item := range pageData.KzoCards {
		comics = append(comics, Comic{Title: fallbackTitle(item.Title, item.URL), URL: item.URL, CoverURL: item.CoverURL, Author: item.Author, Status: item.Status})
	}
	if len(comics) == 0 {
		return libraryResult{}, errors.New("no kzo.moe comic cards found; check login, search or category")
	}
	total := pageData.Total
	pageSize := len(comics)
	totalPages := 1
	if total > 0 && pageSize > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	return libraryResult{Comics: comics, Categories: firstPage.Categories, Page: pageNumber, PageSize: pageSize, Total: total, TotalPages: totalPages}, nil
}

func kzoLibraryURL(cfg Config, pageNumber int, search, category string) string {
	if search != "" {
		query := url.Values{"s": {search}}
		return resolveURL(cfg.UpstreamURL, "/list.php?"+query.Encode())
	}
	base := resolveURL(cfg.UpstreamURL, firstNonEmpty(category, cfg.LibraryPath))
	if pageNumber <= 1 {
		return base
	}
	if category != "" || strings.HasPrefix(mustURLPath(base), "/l/") {
		return kzoNumberedPath(base, pageNumber)
	}
	return resolveURL(cfg.UpstreamURL, fmt.Sprintf("/l/--/%d.htm", pageNumber))
}

func kzoPageURL(first page, firstURL string, pageNumber int) string {
	if pageNumber <= 1 {
		return firstURL
	}
	for _, raw := range first.PageLinks {
		if kzoPageNumber(raw) == pageNumber {
			return raw
		}
	}
	for _, raw := range first.PageLinks {
		if u, err := validHTTPURL(raw); err == nil && kzoListPageRE.MatchString(u.Path) {
			return kzoNumberedPath(raw, pageNumber)
		}
	}
	if strings.HasPrefix(mustURLPath(firstURL), "/list.php") {
		u, err := url.Parse(firstURL)
		if err == nil {
			query := u.Query()
			query.Set("p", strconv.Itoa(pageNumber))
			u.RawQuery = query.Encode()
			return u.String()
		}
	}
	return kzoNumberedPath(firstURL, pageNumber)
}

func kzoNumberedPath(raw string, pageNumber int) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "l" {
		parts[len(parts)-1] = strconv.Itoa(pageNumber) + ".htm"
		u.Path = "/" + strings.Join(parts, "/")
		return u.String()
	}
	return raw
}

func kzoPageNumber(raw string) int {
	u, err := validHTTPURL(raw)
	if err != nil {
		return 0
	}
	if kzoListPageRE.MatchString(u.Path) {
		name := strings.TrimSuffix(filepath.Base(u.Path), ".htm")
		value, _ := strconv.Atoi(name)
		return value
	}
	for _, key := range []string{"p", "page"} {
		value, _ := strconv.Atoi(u.Query().Get(key))
		if value > 0 {
			return value
		}
	}
	return 0
}

func mustURLPath(raw string) string {
	u, _ := url.Parse(raw)
	if u == nil {
		return ""
	}
	return u.Path
}

func (a *App) scanChapters(ctx context.Context, comicURL string) ([]Chapter, error) {
	if isKzoURL(a.config().UpstreamURL) {
		if parsed, err := validHTTPURL(comicURL); err == nil && strings.HasPrefix(parsed.Path, "/c/") {
			return a.scanKzoChapters(ctx, comicURL)
		}
	}
	pageData, err := a.getPage(ctx, comicURL)
	if err != nil {
		return nil, err
	}
	links := filterLinks(pageData.Links, a.config().UpstreamURL, comicURL)
	strong := make([]link, 0, len(links))
	for _, item := range links {
		if hasAny(item.URL, "/chapter", "/episode", "/ep/", "chapter=", "episode=", "/read/") {
			strong = append(strong, item)
		}
	}
	if len(strong) > 0 {
		links = strong
	}
	seen := map[string]bool{}
	chapters := make([]Chapter, 0, len(links))
	for index, item := range links {
		if seen[item.URL] || item.URL == comicURL {
			continue
		}
		seen[item.URL] = true
		chapters = append(chapters, Chapter{Title: fallbackTitle(item.Title, item.URL), URL: item.URL, Order: orderOf(item.Title+" "+item.URL, index+1)})
	}
	if len(chapters) == 0 && len(pageData.Images) > 0 {
		chapters = append(chapters, Chapter{Title: "全本", URL: comicURL, Order: 1})
	}
	sort.SliceStable(chapters, func(i, j int) bool {
		if chapters[i].Order != chapters[j].Order {
			return chapters[i].Order < chapters[j].Order
		}
		return strings.ToLower(chapters[i].Title) < strings.ToLower(chapters[j].Title)
	})
	for i := range chapters {
		chapters[i].Order = i + 1
	}
	return chapters, nil
}

func (a *App) scanKzoChapters(ctx context.Context, comicURL string) ([]Chapter, error) {
	cfg := a.config()
	comicBody, _, err := a.fetch(ctx, comicURL, 24<<20)
	if err != nil {
		return nil, err
	}
	bookIDMatch := kzoBookIDRE.FindSubmatch(comicBody)
	dataURLMatch := kzoBookDataRE.FindSubmatch(comicBody)
	if len(bookIDMatch) < 2 || len(dataURLMatch) < 2 {
		return nil, errors.New("kzo.moe comic page has no book data endpoint")
	}
	dataURL := resolveURL(comicURL, html.UnescapeString(string(dataURLMatch[1])))
	dataBody, _, err := a.fetch(ctx, dataURL, 24<<20)
	if err != nil {
		return nil, err
	}
	chapters := make([]Chapter, 0)
	for index, match := range kzoVolInfoRE.FindAllStringSubmatch(string(dataBody), -1) {
		fields := strings.Split(strings.TrimSpace(match[1]), ",")
		if len(fields) < 15 || strings.TrimSpace(fields[0]) == "" {
			continue
		}
		volumeID := strings.TrimSpace(fields[0])
		title := cleanText(fields[5])
		if title == "" {
			title = "第" + strconv.Itoa(index+1) + "话"
		}
		order := index + 1
		if parsed, parseErr := strconv.Atoi(strings.TrimSpace(fields[4])); parseErr == nil && parsed > 0 {
			order = parsed
		}
		volumeURL, err := url.Parse(comicURL)
		if err != nil {
			continue
		}
		query := volumeURL.Query()
		query.Set("v", volumeID)
		volumeURL.RawQuery = query.Encode()
		downloadQuery := url.Values{"b": {string(bookIDMatch[1])}, "v": {volumeID}, "mobi": {"2"}, "vip": {"0"}, "json": {"1"}}
		chapters = append(chapters, Chapter{
			Title:       title,
			URL:         volumeURL.String(),
			Order:       order,
			DownloadURL: resolveURL(cfg.UpstreamURL, "/getdownurl.php?"+downloadQuery.Encode()),
		})
	}
	if len(chapters) == 0 {
		return nil, errors.New("kzo.moe returned no chapters; the account may have no access or the book data is still loading")
	}
	sort.SliceStable(chapters, func(i, j int) bool { return chapters[i].Order < chapters[j].Order })
	for i := range chapters {
		chapters[i].Order = i + 1
	}
	return a.scanBookofCovers(ctx, comicURL, chapters), nil
}

func (a *App) scanBookofCovers(ctx context.Context, comicURL string, chapters []Chapter) []Chapter {
	bookURL, ok := bookofURLForKzoComic(comicURL)
	if !ok {
		return chapters
	}
	coverCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pageBody, err := a.fetchBookof(coverCtx, bookURL)
	if err != nil {
		return chapters
	}
	dataPath := bookofVolumeDataRE.FindString(string(pageBody))
	if dataPath == "" {
		return chapters
	}
	dataBody, err := a.fetchBookof(coverCtx, resolveURL(bookURL, dataPath))
	if err != nil {
		return chapters
	}
	return applyBookofCovers(a.config(), dataBody, chapters)
}

func applyBookofCovers(cfg Config, dataBody []byte, chapters []Chapter) []Chapter {
	byTitle := make(map[string]string)
	byOrder := make(map[int]string)
	for _, match := range bookofVolumeRE.FindAllStringSubmatch(string(dataBody), -1) {
		if len(match) < 5 {
			continue
		}
		order, _ := strconv.Atoi(match[1])
		title := cleanText(match[2])
		cover := html.UnescapeString(strings.TrimSpace(match[3]))
		if u, urlErr := validHTTPURL(cover); urlErr != nil || !allowedCoverHost(u, cfg.UpstreamURL) {
			cover = ""
		}
		if title != "" && cover != "" {
			byTitle[normalizeVolumeTitle(title)] = cover
		}
		if order > 0 && cover != "" {
			byOrder[order] = cover
		}
	}
	for index := range chapters {
		cover := byTitle[normalizeVolumeTitle(chapters[index].Title)]
		if cover == "" {
			cover = byOrder[index+1]
		}
		chapters[index].CoverURL = cover
	}
	return chapters
}

func bookofURLForKzoComic(comicURL string) (string, bool) {
	u, err := validHTTPURL(comicURL)
	if err != nil {
		return "", false
	}
	match := kzoComicPathRE.FindStringSubmatch(u.Path)
	if len(match) < 2 {
		return "", false
	}
	return "https://bookof.moe/b/" + match[1] + ".htm", true
}

func normalizeVolumeTitle(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), "")
}

func (a *App) fetchBookof(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := validHTTPURL(rawURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "koxmoe-transfer/0.1")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	req.Header.Set("Referer", "https://bookof.moe/")
	response, err := a.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("bookof returned %s", response.Status)
	}
	return io.ReadAll(io.LimitReader(response.Body, 24<<20))
}

func (a *App) getPage(ctx context.Context, rawURL string) (page, error) {
	body, _, err := a.fetch(ctx, rawURL, 24<<20)
	if err != nil {
		return page{}, err
	}
	return parsePage(body, rawURL)
}

func newJob(id string, input downloadRequest) *Job {
	job := &Job{
		ID:        id,
		Status:    "queued",
		Comic:     safeName(input.ComicName),
		CoverURL:  input.CoverURL,
		Total:     len(input.Chapters),
		StartedAt: time.Now(),
		request:   input,
		Chapters:  make([]JobChapter, len(input.Chapters)),
	}
	for index, chapter := range input.Chapters {
		job.Chapters[index] = JobChapter{
			ID:       strconv.Itoa(index),
			Title:    chapter.Title,
			Order:    chapter.Order,
			Status:   "queued",
			CoverURL: chapter.CoverURL,
			chapter:  chapter,
		}
	}
	return job
}

func (a *App) jobSnapshot(id string) (Job, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	job := a.jobs[id]
	if job == nil {
		return Job{}, false
	}
	copy := *job
	copy.Files = append([]string(nil), job.Files...)
	copy.Failures = append([]string(nil), job.Failures...)
	copy.Chapters = append([]JobChapter(nil), job.Chapters...)
	return copy, true
}

func summarizeJob(job *Job) {
	job.Done = 0
	job.BytesDone = 0
	job.BytesTotal = 0
	job.SpeedBPS = 0
	job.Files = nil
	job.Failures = nil
	hasRunning := false
	allDone := len(job.Chapters) > 0
	for _, chapter := range job.Chapters {
		job.BytesDone += chapter.Done
		job.BytesTotal += chapter.Total
		job.SpeedBPS += chapter.SpeedBPS
		switch chapter.Status {
		case "completed":
			job.Done++
			if chapter.File != "" {
				job.Files = append(job.Files, chapter.File)
			}
		case "failed":
			job.Done++
			job.Failures = append(job.Failures, chapter.Title+": "+chapter.Error)
		case "running":
			hasRunning = true
			allDone = false
		default:
			allDone = false
		}
	}
	if allDone {
		if len(job.Failures) > 0 {
			job.Status = "completed_with_errors"
		} else {
			job.Status = "completed"
		}
		if job.FinishedAt.IsZero() {
			job.FinishedAt = time.Now()
		}
	} else if hasRunning {
		job.Status = "running"
		job.FinishedAt = time.Time{}
	} else {
		job.Status = "queued"
		job.FinishedAt = time.Time{}
	}
}

func (a *App) updateJobChapter(jobID, chapterID string, update func(*Job, *JobChapter)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	job := a.jobs[jobID]
	if job == nil {
		return
	}
	for index := range job.Chapters {
		chapter := &job.Chapters[index]
		if chapter.ID != chapterID {
			continue
		}
		update(job, chapter)
		summarizeJob(job)
		return
	}
}

func (a *App) downloadWorker() {
	for task := range a.downloadQueue {
		a.updateJobChapter(task.JobID, task.ChapterID, func(job *Job, chapter *JobChapter) {
			chapter.Status = "running"
			chapter.StartedAt = time.Now()
			job.Error = ""
		})
		cfg := a.config()
		filename, err := a.downloadChapter(context.Background(), cfg, task.Request.ComicName, task.Request.Category, task.Request.ComicURL, task.Chapter, func(done, total int64) {
			a.updateJobChapter(task.JobID, task.ChapterID, func(_ *Job, chapter *JobChapter) {
				chapter.Done = done
				chapter.Total = maxInt64(total, 0)
				if !chapter.StartedAt.IsZero() {
					elapsed := time.Since(chapter.StartedAt).Seconds()
					if elapsed > 0 {
						chapter.SpeedBPS = int64(float64(done) / elapsed)
					}
				}
			})
		})
		a.updateJobChapter(task.JobID, task.ChapterID, func(_ *Job, chapter *JobChapter) {
			chapter.FinishedAt = time.Now()
			if err != nil {
				chapter.Status = "failed"
				chapter.Error = err.Error()
				return
			}
			chapter.Status = "completed"
			chapter.File = filename
			if chapter.Total > 0 {
				chapter.Done = chapter.Total
			}
		})
	}
}

type progressFunc func(done, total int64)

type progressSnapshot struct {
	Done  int64
	Total int64
}

func maxInt64(value, minimum int64) int64 {
	if value < minimum {
		return minimum
	}
	return value
}

func (a *App) downloadChapter(ctx context.Context, cfg Config, comicName, category, comicURL string, chapter Chapter, progress progressFunc) (string, error) {
	if chapter.DownloadURL != "" {
		return a.downloadKzoEPUB(ctx, cfg, comicName, category, chapter, progress)
	}
	data, _, err := a.fetch(ctx, chapter.URL, 24<<20)
	if err != nil {
		return "", err
	}
	p, err := parsePage(data, chapter.URL)
	if err != nil {
		return "", err
	}
	if len(p.Images) == 0 {
		return "", errors.New("no images found")
	}
	images := make([]epubImage, len(p.Images))
	workers := cfg.Workers
	if workers > len(images) {
		workers = len(images)
	}
	queue := make(chan int)
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	var progressMu sync.Mutex
	imageProgress := make([]progressSnapshot, len(images))
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range queue {
				imageURL := p.Images[index].URL
				body, contentType, fetchErr := a.fetchProgress(ctx, imageURL, 80<<20, func(done, total int64) {
					if progress == nil {
						return
					}
					progressMu.Lock()
					imageProgress[index] = progressSnapshot{Done: done, Total: maxInt64(total, 0)}
					var bytesDone, bytesTotal int64
					for _, imageProgress := range imageProgress {
						bytesDone += imageProgress.Done
						bytesTotal += imageProgress.Total
					}
					progressMu.Unlock()
					progress(bytesDone, bytesTotal)
				})
				if fetchErr != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = fetchErr
					}
					errMu.Unlock()
					continue
				}
				images[index] = epubImage{Data: body, ContentType: contentType, URL: imageURL}
			}
		}()
	}
	for index := range images {
		queue <- index
	}
	close(queue)
	wg.Wait()
	if firstErr != nil {
		return "", firstErr
	}

	dir, filename := chapterOutput(cfg, category, comicName, chapter.Title)
	return saveFile(dir, filename, func(file *os.File) error {
		return writeEPUB(file, safeName(comicName), chapter, images, comicURL)
	})
}

func (a *App) downloadKzoEPUB(ctx context.Context, cfg Config, comicName, category string, chapter Chapter, progress progressFunc) (string, error) {
	data, err := a.fetchKzoEPUB(ctx, chapter.DownloadURL, cfg.UpstreamURL, progress)
	if err != nil {
		alternateURL := alternateKzoDownloadURL(chapter.DownloadURL)
		if alternateURL != "" {
			if alternateData, alternateErr := a.fetchKzoEPUB(ctx, alternateURL, cfg.UpstreamURL, progress); alternateErr == nil {
				data, err = alternateData, nil
			} else {
				err = fmt.Errorf("download line 1: %v; line 2: %v", err, alternateErr)
			}
		}
		if err != nil {
			return "", err
		}
	}
	dir, filename := chapterOutput(cfg, category, comicName, chapter.Title)
	return saveFile(dir, filename, func(file *os.File) error {
		_, err := file.Write(data)
		return err
	})
}

func downloadDir(cfg Config, category, comicName string) string {
	category = strings.TrimSpace(category)
	if category == "" {
		category = "未分类"
	}
	return filepath.Join(cfg.NASDir, safeName(category), safeName(comicName))
}

func chapterOutput(cfg Config, category, comicName, chapterTitle string) (string, string) {
	title := strings.Join(strings.Fields(strings.TrimSpace(chapterTitle)), "")
	return downloadDir(cfg, category, comicName), fmt.Sprintf("[Kmoe][%s]%s.epub", safeName(comicName), safeName(title))
}

func downloadedFiles(cfg Config, category, comicName string) (map[string]bool, error) {
	files := make(map[string]bool)
	entries, err := os.ReadDir(downloadDir(cfg, category, comicName))
	if errors.Is(err, os.ErrNotExist) {
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".epub") {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.Mode().IsRegular() {
			files[entry.Name()] = true
		}
	}
	return files, nil
}

func annotateDownloaded(cfg Config, category, comicName string, chapters []Chapter) ([]Chapter, error) {
	files, err := downloadedFiles(cfg, category, comicName)
	if err != nil {
		return nil, err
	}
	for index := range chapters {
		_, chapters[index].FileName = chapterOutput(cfg, category, comicName, chapters[index].Title)
		chapters[index].Downloaded = files[chapters[index].FileName]
	}
	return chapters, nil
}

func (a *App) fetchKzoEPUB(ctx context.Context, downloadURL, baseURL string, progress progressFunc) ([]byte, error) {
	data, _, err := a.fetchProgress(ctx, downloadURL, 2<<20, progress)
	if err != nil {
		return nil, err
	}
	if isZip(data) {
		return data, nil
	}
	directURL, err := parseDownloadURL(data, baseURL)
	if err != nil {
		return nil, err
	}
	data, _, err = a.fetchProgress(ctx, directURL, 200<<20, progress)
	if err != nil {
		return nil, err
	}
	if !isZip(data) {
		return nil, errors.New("kzo.moe returned a non-EPUB download")
	}
	return data, nil
}

func alternateKzoDownloadURL(rawURL string) string {
	u, err := validHTTPURL(rawURL)
	if err != nil {
		return ""
	}
	query := u.Query()
	if query.Get("vip") == "1" {
		return ""
	}
	query.Set("vip", "1")
	u.RawQuery = query.Encode()
	return u.String()
}

func parseDownloadURL(data []byte, baseURL string) (string, error) {
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("unexpected download response: %s", strings.TrimSpace(string(data)))
	}
	var find func(any) string
	find = func(value any) string {
		switch typed := value.(type) {
		case string:
			if strings.HasPrefix(typed, "http://") || strings.HasPrefix(typed, "https://") || strings.HasPrefix(typed, "/dl/") {
				return resolveURL(baseURL, typed)
			}
		case []any:
			for _, item := range typed {
				if result := find(item); result != "" {
					return result
				}
			}
		case map[string]any:
			for key, item := range typed {
				if strings.Contains(strings.ToLower(key), "url") {
					if result := find(item); result != "" {
						return result
					}
				}
			}
		}
		return ""
	}
	if result := find(payload); result != "" {
		return result, nil
	}
	if result, ok := payload.(map[string]any); ok && result["msg"] != nil {
		return "", errors.New(fmt.Sprint(result["msg"]))
	}
	return "", errors.New("kzo.moe returned no download URL; the account may need quota or CAPTCHA")
}

func isZip(data []byte) bool {
	if len(data) < 4 || !bytes.Equal(data[:2], []byte("PK")) {
		return false
	}
	_, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	return err == nil
}

func saveFile(dir, filename string, write func(*os.File) error) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, filename)
	tmp, err := os.CreateTemp(dir, ".epub-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := write(tmp); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", err
	}
	return path, nil
}

type epubImage struct {
	Data        []byte
	ContentType string
	URL         string
}

func writeEPUB(file *os.File, title string, chapter Chapter, images []epubImage, sourceURL string) error {
	archive := zip.NewWriter(file)
	mimetypeHeader := &zip.FileHeader{Name: "mimetype", Method: zip.Store}
	mimetypeHeader.SetModTime(time.Unix(0, 0))
	entry, err := archive.CreateHeader(mimetypeHeader)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(entry, "application/epub+zip"); err != nil {
		return err
	}
	write := func(name, content string) error {
		entry, err := archive.Create(name)
		if err != nil {
			return err
		}
		_, err = io.WriteString(entry, content)
		return err
	}
	if err := write("META-INF/container.xml", `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`); err != nil {
		return err
	}

	manifest := []string{`<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>`, `<item id="style" href="style.css" media-type="text/css"/>`, `<item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>`}
	body := make([]string, 0, len(images))
	for i, image := range images {
		ext := imageExtension(image.ContentType, image.URL)
		name := fmt.Sprintf("images/image-%03d%s", i+1, ext)
		entry, err := archive.CreateHeader(&zip.FileHeader{Name: "OEBPS/" + name, Method: zip.Store})
		if err != nil {
			return err
		}
		if _, err := entry.Write(image.Data); err != nil {
			return err
		}
		mediaType := imageMediaType(image.ContentType, ext)
		manifest = append(manifest, fmt.Sprintf(`<item id="image-%d" href="%s" media-type="%s"/>`, i+1, name, mediaType))
		body = append(body, fmt.Sprintf(`<img src="%s" alt="%d"/>`, html.EscapeString(name), i+1))
	}
	if err := write("OEBPS/style.css", `body{margin:0;padding:0;text-align:center;background:#111;color:#eee}img{display:block;width:auto;max-width:100%;height:auto;margin:0 auto}`); err != nil {
		return err
	}
	xhtml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>%s</title><link rel="stylesheet" href="style.css" type="text/css"/></head><body>%s</body></html>`, html.EscapeString(chapter.Title), strings.Join(body, "\n"))
	if err := write("OEBPS/chapter.xhtml", xhtml); err != nil {
		return err
	}
	nav := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title>%s</title></head><body><nav epub:type="toc"><ol><li><a href="chapter.xhtml">%s</a></li></ol></nav></body></html>`, html.EscapeString(title), html.EscapeString(chapter.Title))
	if err := write("OEBPS/nav.xhtml", nav); err != nil {
		return err
	}
	identifier := fmt.Sprintf("urn:sha1:%x", sha1.Sum([]byte(sourceURL+"\n"+chapter.Title)))
	opf := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="book-id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="book-id">%s</dc:identifier><dc:title>%s - %s</dc:title><dc:language>zh</dc:language></metadata><manifest>%s</manifest><spine><itemref idref="chapter"/></spine></package>`, identifier, html.EscapeString(title), html.EscapeString(chapter.Title), strings.Join(manifest, ""))
	if err := write("OEBPS/content.opf", opf); err != nil {
		return err
	}
	return archive.Close()
}

func (a *App) fetch(ctx context.Context, rawURL string, maxBytes int64) ([]byte, string, error) {
	return a.fetchProgress(ctx, rawURL, maxBytes, nil)
}

func (a *App) fetchProgress(ctx context.Context, rawURL string, maxBytes int64, progress progressFunc) ([]byte, string, error) {
	u, err := validHTTPURL(rawURL)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "koxmoe-transfer/0.1")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	req.Header.Set("Referer", strings.TrimRight(a.config().UpstreamURL, "/")+"/")
	resp, err := a.httpClient().Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		message := ""
		if match := titleRE.FindStringSubmatch(string(detail)); len(match) > 1 {
			message = cleanText(match[1])
		}
		if message != "" {
			return nil, "", fmt.Errorf("upstream returned %s for %s: %s", resp.Status, u.Path, message)
		}
		return nil, "", fmt.Errorf("upstream returned %s for %s", resp.Status, u.Path)
	}
	var body bytes.Buffer
	reader := io.LimitReader(resp.Body, maxBytes+1)
	buffer := make([]byte, 32<<10)
	var bytesRead int64
	for {
		count, readErr := reader.Read(buffer)
		if count > 0 {
			if _, err := body.Write(buffer[:count]); err != nil {
				return nil, "", err
			}
			bytesRead += int64(count)
			if progress != nil {
				progress(bytesRead, resp.ContentLength)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, "", readErr
		}
	}
	if int64(body.Len()) > maxBytes {
		return nil, "", fmt.Errorf("response too large: %s", u.Path)
	}
	return body.Bytes(), resp.Header.Get("Content-Type"), nil
}

func parsePage(body []byte, baseURL string) (page, error) {
	base, err := validHTTPURL(baseURL)
	if err != nil {
		return page{}, err
	}
	data := string(body)
	result := page{Title: fallbackTitle("", base.String())}
	if match := titleRE.FindStringSubmatch(data); len(match) > 1 {
		result.Title = cleanText(match[1])
	} else if match := h1RE.FindStringSubmatch(data); len(match) > 1 {
		result.Title = cleanText(match[1])
	}
	for _, match := range anchorRE.FindAllStringSubmatch(data, -1) {
		attrs := attributes(match[1])
		href := attrs["href"]
		if href == "" {
			continue
		}
		if resolved := resolveHTTPURL(base, href); resolved != "" {
			text := cleanText(match[2])
			if text == "" {
				text = cleanText(attrs["title"])
			}
			result.Links = append(result.Links, link{Title: text, URL: resolved})
		}
	}
	for _, match := range imageRE.FindAllStringSubmatch(data, -1) {
		attrs := attributes(match[1])
		src := firstNonEmpty(attrs["data-src"], attrs["data-original"], attrs["src"])
		if src == "" && attrs["srcset"] != "" {
			src = strings.Fields(strings.Split(attrs["srcset"], ",")[0])[0]
		}
		if resolved := resolveHTTPURL(base, src); resolved != "" {
			result.Images = append(result.Images, imageRef{URL: resolved})
		}
	}
	if isKzoURL(base.String()) {
		result.KzoCards = parseKzoCards(data, base.String())
		result.Categories = parseKzoCategories(data, base.String())
		result.PageLinks = parseKzoPageLinks(data, base.String())
		result.Total = parseKzoTotal(data)
		result.Links = append(result.Links, result.KzoCards...)
	}
	result.Links = uniqueLinks(result.Links)
	result.Images = uniqueImages(result.Images)
	return result, nil
}

func parseComicDetail(body []byte, baseURL string) (ComicDetail, error) {
	base, err := validHTTPURL(baseURL)
	if err != nil {
		return ComicDetail{}, err
	}
	detail := ComicDetail{Comic: Comic{Title: fallbackTitle("", base.String()), URL: base.String()}}
	data := string(body)
	if match := kzoDetailTitleRE.FindStringSubmatch(data); len(match) > 1 {
		detail.Title = cleanText(match[1])
	}
	if match := kzoDetailAuthorRE.FindStringSubmatch(data); len(match) > 1 {
		detail.Author = cleanText(match[1])
	}
	if match := kzoDetailAliasRE.FindStringSubmatch(data); len(match) > 1 {
		detail.Aliases = cleanText(match[1])
	}
	if match := kzoDetailMetaRE.FindStringSubmatch(data); len(match) > 5 {
		detail.Status = cleanText(match[1])
		detail.Region = cleanText(match[2])
		detail.Language = cleanText(match[3])
		detail.LastPublished = cleanText(match[4])
		detail.Updated = cleanText(match[5])
	}
	if match := kzoDetailVersionRE.FindStringSubmatch(data); len(match) > 2 {
		detail.Version = cleanText(match[1])
		detail.ScannedBy = cleanText(match[2])
	}
	if match := kzoDetailStatsRE.FindStringSubmatch(data); len(match) > 4 {
		detail.Subscribers = parsePositiveInt(match[1])
		detail.Favorites = parsePositiveInt(match[2])
		detail.ReadCount = parsePositiveInt(match[3])
		detail.Heat = parsePositiveInt(match[4])
	}
	if match := kzoDetailScoreRE.FindStringSubmatch(data); len(match) > 2 {
		detail.Score, _ = strconv.ParseFloat(match[1], 64)
		detail.ScoreCount = parsePositiveInt(match[2])
	}
	if match := kzoDetailTagsRE.FindStringSubmatch(data); len(match) > 1 {
		for _, token := range strings.Fields(cleanText(match[1])) {
			if strings.HasPrefix(token, "(") || strings.HasPrefix(token, "（") || token == "分類：" {
				continue
			}
			detail.Tags = append(detail.Tags, token)
		}
	}
	if match := kzoDetailDescJSRE.FindStringSubmatch(data); len(match) > 1 {
		if decoded, decodeErr := strconv.Unquote(`"` + match[1] + `"`); decodeErr == nil {
			detail.Description = cleanText(decoded)
		}
	}
	if detail.Description == "" || strings.Contains(detail.Description, "請訪問 https://kzo.moe") {
		if match := kzoDetailDescRE.FindStringSubmatch(data); len(match) > 1 {
			detail.Description = cleanText(match[1])
		}
	}
	for _, match := range imageRE.FindAllStringSubmatch(data, -1) {
		attrs := attributes(match[1])
		if strings.Contains(strings.ToLower(attrs["class"]), "img_book") {
			detail.CoverURL = resolveURL(base.String(), attrs["src"])
			break
		}
	}
	return detail, nil
}

func parsePositiveInt(value string) int {
	result, _ := strconv.Atoi(strings.TrimSpace(value))
	if result < 0 {
		return 0
	}
	return result
}

func parseKzoCards(data, baseURL string) []link {
	result := make([]link, 0)
	for _, match := range kzoCardRE.FindAllStringSubmatch(data, -1) {
		values := jsStrings(match[1])
		if len(values) < 11 || !strings.Contains(values[2], "/c/") {
			continue
		}
		resolved := resolveURL(baseURL, values[2])
		if resolved == "" {
			continue
		}
		coverURL := resolveURL(baseURL, values[3])
		if coverURL == "" {
			coverURL = values[3]
		}
		result = append(result, link{Title: values[10], URL: resolved, CoverURL: coverURL, Author: values[11], Status: values[12]})
	}
	return result
}

func parseKzoCategories(data, baseURL string) []KzoCategory {
	seen := map[string]bool{}
	result := make([]KzoCategory, 0)
	for _, match := range anchorRE.FindAllStringSubmatch(data, -1) {
		attrs := attributes(match[1])
		if !strings.HasPrefix(attrs["id"], "sel_type_") {
			continue
		}
		resolved := resolveURL(baseURL, attrs["href"])
		name := cleanText(match[2])
		if resolved == "" || name == "" || seen[resolved] {
			continue
		}
		seen[resolved] = true
		result = append(result, KzoCategory{Name: name, URL: resolved})
	}
	return result
}

func parseKzoPageLinks(data, baseURL string) []string {
	seen := map[string]bool{}
	result := make([]string, 0)
	for _, match := range kzoPageRE.FindAllStringSubmatch(data, -1) {
		for _, value := range jsStrings(match[1]) {
			if !strings.Contains(value, "/l/") && !strings.Contains(value, "/list.php") {
				continue
			}
			resolved := resolveURL(baseURL, value)
			if resolved != "" && !seen[resolved] {
				seen[resolved] = true
				result = append(result, resolved)
			}
		}
	}
	return result
}

func parseKzoTotal(data string) int {
	match := kzoTotalRE.FindStringSubmatch(data)
	if len(match) < 2 {
		return 0
	}
	total, _ := strconv.Atoi(match[1])
	return total
}

func jsStrings(value string) []string {
	result := make([]string, 0)
	for _, raw := range jsStringRE.FindAllString(value, -1) {
		if strings.HasPrefix(raw, "\"") {
			if parsed, err := strconv.Unquote(raw); err == nil {
				result = append(result, parsed)
			}
		}
	}
	return result
}

func parseLoginForm(body []byte, baseURL string) (loginForm, error) {
	for _, match := range formRE.FindAllStringSubmatch(string(body), -1) {
		formAttrs := attributes(match[1])
		values := url.Values{}
		usernameField, passwordField, fallbackField := "", "", ""
		for _, input := range inputRE.FindAllStringSubmatch(match[2], -1) {
			attrs := attributes(input[1])
			name := strings.TrimSpace(attrs["name"])
			if name == "" {
				continue
			}
			inputType := strings.ToLower(attrs["type"])
			if inputType == "hidden" {
				values.Set(name, attrs["value"])
				continue
			}
			if inputType == "password" || strings.Contains(strings.ToLower(name), "pass") {
				passwordField = name
				continue
			}
			if isUsernameField(name) {
				usernameField = name
			} else if fallbackField == "" && inputType != "submit" && inputType != "button" && inputType != "checkbox" {
				fallbackField = name
			}
		}
		if passwordField == "" {
			continue
		}
		if usernameField == "" {
			usernameField = fallbackField
		}
		if usernameField == "" {
			return loginForm{}, errors.New("login form has no username field")
		}
		action := resolveURL(baseURL, formAttrs["action"])
		if action == "" {
			action = baseURL
		}
		method := strings.ToUpper(formAttrs["method"])
		if method != http.MethodGet {
			method = http.MethodPost
		}
		return loginForm{Action: action, Method: method, UsernameField: usernameField, PasswordField: passwordField, Values: values}, nil
	}
	return loginForm{}, errors.New("login form not found; check LOGIN_PATH or the site's login page")
}

func isUsernameField(name string) bool {
	name = strings.ToLower(name)
	return strings.Contains(name, "user") || strings.Contains(name, "email") || strings.Contains(name, "account") || strings.Contains(name, "login") || strings.Contains(name, "identifier")
}

func attributes(raw string) map[string]string {
	result := make(map[string]string)
	for _, match := range attrRE.FindAllStringSubmatch(raw, -1) {
		result[strings.ToLower(match[1])] = html.UnescapeString(strings.TrimSpace(match[2]))
	}
	return result
}

func filterLinks(links []link, upstream, current string) []link {
	base, err := validHTTPURL(upstream)
	if err != nil {
		return nil
	}
	currentURL := resolveURL(upstream, current)
	result := make([]link, 0, len(links))
	for _, item := range links {
		u, err := validHTTPURL(item.URL)
		if err != nil || u.Hostname() != base.Hostname() || u.String() == currentURL || ignoredLink(u) {
			continue
		}
		result = append(result, item)
	}
	return result
}

func ignoredLink(u *url.URL) bool {
	path := strings.ToLower(u.Path)
	return strings.HasPrefix(path, "/_next") || strings.HasPrefix(path, "/assets") || hasAny(path, ".css", ".js", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", "/login", "/logout", "/register")
}

func uniqueLinks(items []link) []link {
	seen := map[string]bool{}
	result := make([]link, 0, len(items))
	for _, item := range items {
		if !seen[item.URL] {
			seen[item.URL] = true
			result = append(result, item)
		}
	}
	return result
}

func uniqueImages(items []imageRef) []imageRef {
	seen := map[string]bool{}
	result := make([]imageRef, 0, len(items))
	for _, item := range items {
		if !seen[item.URL] {
			seen[item.URL] = true
			result = append(result, item)
		}
	}
	return result
}

func validHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("invalid http url")
	}
	u.Fragment = ""
	return u, nil
}

func sameHost(first, second string) bool {
	a, errA := validHTTPURL(first)
	b, errB := validHTTPURL(second)
	return errA == nil && errB == nil && a.Hostname() == b.Hostname()
}

func isKzoURL(raw string) bool {
	u, err := validHTTPURL(raw)
	if err != nil {
		return false
	}
	return u.Hostname() == "kzo.moe" || strings.HasSuffix(u.Hostname(), ".kzo.moe")
}

func resolveURL(base, ref string) string {
	baseURL, err := validHTTPURL(base)
	if err != nil {
		return ""
	}
	return resolveHTTPURL(baseURL, ref)
}

func resolveHTTPURL(base *url.URL, ref string) string {
	refURL, err := url.Parse(strings.TrimSpace(ref))
	if err != nil || ref == "" || refURL.Scheme == "javascript" || refURL.Scheme == "mailto" {
		return ""
	}
	resolved := base.ResolveReference(refURL)
	if resolved.Scheme != "http" && resolved.Scheme != "https" || resolved.Host == "" {
		return ""
	}
	resolved.Fragment = ""
	return resolved.String()
}

func fallbackTitle(title, rawURL string) string {
	if clean := cleanText(title); clean != "" {
		return clean
	}
	u, err := url.Parse(rawURL)
	if err == nil {
		if value := strings.Trim(u.Path, "/"); value != "" {
			parts := strings.Split(value, "/")
			return cleanText(parts[len(parts)-1])
		}
	}
	return "未命名"
}

func cleanText(value string) string {
	value = html.UnescapeString(tagRE.ReplaceAllString(value, " "))
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func orderOf(value string, fallback int) int {
	match := numberRE.FindStringSubmatch(strings.ToLower(value))
	if len(match) < 2 {
		return fallback
	}
	number, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return fallback
	}
	return int(number * 100)
}

func safeName(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, value)
	value = strings.Trim(value, " .")
	if value == "" || value == "." || value == ".." {
		return "未命名"
	}
	return value
}

func imageExtension(contentType, rawURL string) string {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if extensions, _ := mime.ExtensionsByType(contentType); len(extensions) > 0 {
		return extensions[0]
	}
	u, _ := url.Parse(rawURL)
	ext := strings.ToLower(filepath.Ext(u.Path))
	if ext == ".jpeg" {
		return ".jpg"
	}
	if ext == ".jpg" || ext == ".png" || ext == ".gif" || ext == ".webp" {
		return ext
	}
	return ".jpg"
}

func imageMediaType(contentType, ext string) string {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if strings.HasPrefix(contentType, "image/") {
		return contentType
	}
	return map[string]string{".jpg": "image/jpeg", ".png": "image/png", ".gif": "image/gif", ".webp": "image/webp"}[ext]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func hasAny(value string, parts ...string) bool {
	value = strings.ToLower(value)
	for _, part := range parts {
		if strings.Contains(value, strings.ToLower(part)) {
			return true
		}
	}
	return false
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(started).Round(time.Millisecond))
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
