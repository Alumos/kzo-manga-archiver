const state = { comics: [], categories: [], page: 1, totalPages: 1, total: 0, search: "", category: "", selectedComic: null, detail: null, chapters: [], downloadCategory: "", selected: new Set(), auth: {}, jobs: [], openJobs: new Set(), closedJobs: new Set(), downloadedRefreshPending: false, view: "library", transition: 0 };

const $ = (id) => document.getElementById(id);

async function api(path, options) {
  const response = await fetch(path, options);
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = new Error(data.error || `请求失败（${response.status}）`);
    error.status = response.status;
    throw error;
  }
  return data;
}

function showNotice(message) {
  $("notice").textContent = message || "";
  $("notice").classList.toggle("hidden", !message);
}

function setLoading(button, loading, text) {
  button.disabled = loading;
  if (loading) {
    button.dataset.label = button.textContent;
    button.textContent = text;
  } else if (button.dataset.label) {
    button.textContent = button.dataset.label;
  }
}

function switchView(name, direction = "forward") {
  if (state.view === name) return;
  const current = $(state.view + "View");
  const next = $(name + "View");
  if (!next) return;
  document.querySelectorAll(".view").forEach((view) => view.classList.remove("leaving", "leave-forward", "leave-back", "enter-forward", "enter-back"));
  const token = ++state.transition;
  if (current) current.classList.add("leaving", `leave-${direction}`);
  next.classList.add("active", `enter-${direction}`);
  state.view = name;
  window.scrollTo({ top: 0, behavior: "smooth" });
  window.setTimeout(() => {
    if (token !== state.transition) return;
    if (current) current.classList.remove("active", "leaving", `leave-${direction}`);
    next.classList.remove(`enter-${direction}`);
  }, 460);
}

async function loadAuth() {
  state.auth = await api("/api/auth/status");
  const authenticated = Boolean(state.auth.authenticated);
  const badge = $("authBadge");
  badge.textContent = authenticated ? `已登录：${state.auth.username || "账号"}` : "未登录";
  badge.className = `badge ${authenticated ? "success" : "muted"}`;
  $("loginButton").classList.toggle("hidden", authenticated);
  $("logoutButton").classList.toggle("hidden", !authenticated);
  $("clearCompletedButton").disabled = !authenticated || !state.jobs.some((job) => job.status === "completed");
}

async function syncLibrary(resetPage = false) {
  const button = $("syncButton");
  if (resetPage) state.page = 1;
  switchView("library", "back");
  setLoading(button, true, "同步中…");
  showNotice("");
  try {
    const params = new URLSearchParams({ page: String(state.page) });
    if (state.search) params.set("search", state.search);
    if (state.category) params.set("category", state.category);
    const data = await api(`/api/library?${params}`);
    state.comics = data.comics || [];
    state.categories = data.categories || state.categories;
    state.page = data.page || state.page;
    state.totalPages = data.totalPages || 1;
    state.total = data.total ?? state.comics.length;
    state.selectedComic = null;
    state.detail = null;
    state.chapters = [];
    state.downloadCategory = "";
    state.selected.clear();
    renderCategories();
    renderComics();
    renderDetailShell();
    renderChapters();
  } catch (error) {
    showNotice(error.message);
    if (error.status === 401) openLogin();
  } finally {
    setLoading(button, false);
  }
}

function renderComics() {
  const root = $("comics");
  $("comicCount").textContent = state.total || state.comics.length;
  root.replaceChildren();
  if (!state.comics.length) {
    root.className = "comic-grid empty-state";
    root.textContent = state.auth.authenticated ? "没有找到漫画" : "登录后点击“同步漫画”获取列表";
    renderPagination();
    return;
  }
  root.className = "comic-grid";
  state.comics.forEach((comic) => {
    const button = document.createElement("button");
    button.className = `comic-card ${state.selectedComic?.url === comic.url ? "active" : ""}`;
    button.type = "button";
    button.onclick = () => selectComic(comic);
    if (comic.coverUrl) {
      const cover = document.createElement("img");
      cover.className = "comic-cover";
      cover.loading = "lazy";
      cover.decoding = "async";
      cover.alt = comic.title;
      cover.src = `/api/cover?url=${encodeURIComponent(comic.coverUrl)}`;
      cover.onerror = () => { cover.className = "comic-cover missing"; cover.removeAttribute("src"); cover.alt = "无封面"; };
      button.append(cover);
    } else {
      const cover = document.createElement("div");
      cover.className = "comic-cover missing";
      cover.textContent = "无封面";
      button.append(cover);
    }
    const info = document.createElement("span");
    info.className = "comic-info";
    const title = document.createElement("strong");
    title.textContent = comic.title;
    const meta = document.createElement("small");
    meta.textContent = [comic.author, comic.status].filter(Boolean).join(" · ") || new URL(comic.url).pathname;
    info.append(title, meta);
    button.append(info);
    root.append(button);
  });
  renderPagination();
}

function renderCategories() {
  const select = $("categorySelect");
  select.replaceChildren(new Option("全部分类", ""));
  state.categories.forEach((category) => select.append(new Option(category.name, category.url)));
  select.value = state.category;
}

function renderPagination() {
  const root = $("libraryPagination");
  root.replaceChildren();
  if (state.totalPages <= 1) return;
  const add = (label, page, disabled = false, active = false) => {
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = label;
    button.disabled = disabled;
    button.className = active ? "active" : "";
    button.onclick = () => { state.page = page; syncLibrary(); };
    root.append(button);
  };
  add("上一页", state.page - 1, state.page <= 1);
  const start = Math.max(1, state.page - 2);
  const end = Math.min(state.totalPages, start + 4);
  for (let page = start; page <= end; page += 1) add(String(page), page, false, page === state.page);
  add("下一页", state.page + 1, state.page >= state.totalPages);
}

async function selectComic(comic) {
  state.selectedComic = comic;
  state.detail = null;
  state.chapters = [];
  state.downloadCategory = "";
  state.selected.clear();
  renderDetailShell();
  switchView("detail", "forward");
  try {
    state.detail = await api(`/api/comic?url=${encodeURIComponent(comic.url)}`);
    if (state.selectedComic?.url === comic.url) renderDetail();
  } catch (error) {
    showNotice(error.message);
    if (error.status === 401) openLogin();
  }
}

function renderDetailShell() {
  const comic = state.selectedComic;
  $("detailTitle").textContent = comic?.title || "—";
  $("detailAliases").textContent = "";
  $("detailDescription").textContent = comic ? "加载资料中…" : "选择漫画后查看资料";
  $("detailScore").textContent = "—";
  $("detailScoreCount").textContent = "暂无评分";
  $("detailTags").replaceChildren();
  $("detailFacts").replaceChildren();
  const cover = $("detailCover");
  if (comic?.coverUrl) {
    cover.src = `/api/cover?url=${encodeURIComponent(comic.coverUrl)}`;
    cover.alt = comic.title;
    cover.classList.remove("hidden");
  } else {
    cover.removeAttribute("src");
    cover.classList.add("hidden");
  }
}

function renderDetail() {
  const detail = state.detail;
  if (!detail) return;
  $("detailTitle").textContent = detail.title || state.selectedComic?.title || "—";
  $("detailAliases").textContent = detail.aliases || "";
  const cover = $("detailCover");
  if (detail.coverUrl) {
    cover.src = `/api/cover?url=${encodeURIComponent(detail.coverUrl)}`;
    cover.alt = detail.title || "漫画封面";
    cover.classList.remove("hidden");
  }
  $("detailScore").textContent = detail.score ? `${Number(detail.score).toFixed(1)} 分` : "—";
  $("detailScoreCount").textContent = detail.scoreCount ? `${detail.scoreCount} 人评价` : "暂无评分";
  const tags = $("detailTags");
  tags.replaceChildren();
  (detail.tags || []).forEach((tag) => {
    const item = document.createElement("span");
    item.className = "tag";
    item.textContent = tag;
    tags.append(item);
  });
  const facts = $("detailFacts");
  facts.replaceChildren();
  [["作者", detail.author], ["状态", detail.status], ["地区", detail.region], ["语言", detail.language], ["最后出版", detail.lastPublished], ["更新", detail.updated], ["版本", detail.version], ["扫者", detail.scannedBy], ["订阅", detail.subscribers], ["收藏", detail.favorites], ["读过", detail.readCount], ["热度", detail.heat]].forEach(([label, value]) => {
    if (value === undefined || value === null || value === "") return;
    const item = document.createElement("div");
    const name = document.createElement("span");
    name.textContent = label;
    const content = document.createElement("strong");
    content.textContent = value;
    item.append(name, content);
    facts.append(item);
  });
  $("detailDescription").textContent = detail.description || "暂无简介";
}

async function loadChapters() {
  if (!state.selectedComic) {
    renderChapters();
    return;
  }
  $("downloadHeading").textContent = state.selectedComic.title;
  $("chapters").className = "chapter-list empty-state";
  $("chapters").textContent = "加载章节中…";
  try {
    const params = new URLSearchParams({ url: state.selectedComic.url, comicName: state.selectedComic.title, category: state.detail?.tags?.[0] || "" });
    const data = await api("/api/chapters?" + params);
    state.chapters = data.chapters || [];
    state.downloadCategory = data.category || state.detail?.tags?.[0] || "未分类";
    state.selected = new Set(state.chapters.filter((chapter) => !chapter.downloaded).map((chapter) => chapter.url));
    renderChapters();
  } catch (error) {
    state.chapters = [];
    state.selected.clear();
    renderChapters();
    showNotice(error.message);
    if (error.status === 401) openLogin();
  }
}

async function refreshDownloaded() {
  if (state.downloadedRefreshPending || state.view !== "download" || !state.selectedComic || !state.chapters.length) return;
  state.downloadedRefreshPending = true;
  try {
    const params = new URLSearchParams({ url: state.selectedComic.url, comicName: state.selectedComic.title, category: state.downloadCategory || state.detail?.tags?.[0] || "" });
    const data = await api("/api/downloaded?" + params);
    const files = new Set(data.files || []);
    let changed = false;
    state.chapters.forEach((chapter) => {
      const downloaded = Boolean(chapter.fileName && files.has(chapter.fileName));
      if (downloaded !== Boolean(chapter.downloaded)) changed = true;
      chapter.downloaded = downloaded;
    });
    const nextSelected = new Set([...state.selected].filter((url) => !state.chapters.find((chapter) => chapter.url === url && chapter.downloaded)));
    if (nextSelected.size !== state.selected.size) changed = true;
    state.selected = nextSelected;
    if (changed) renderChapters();
  } catch (_) {
    // Keep the jobs page usable if the NAS directory is temporarily unavailable.
  } finally {
    state.downloadedRefreshPending = false;
  }
}

async function openDownloadView() {
  switchView("download", "forward");
  await loadChapters();
}

function renderChapters() {
  const root = $("chapters");
  root.replaceChildren();
  $("downloadHeading").textContent = state.selectedComic?.title || "选择漫画";
  const downloadedCount = state.chapters.filter((chapter) => chapter.downloaded).length;
  const downloadable = state.chapters.filter((chapter) => !chapter.downloaded);
  $("chapterCount").textContent = `${state.chapters.length} 章 · ${downloadedCount} 已下载`;
  $("selectedCount").textContent = `已选 ${state.selected.size} 章`;
  $("selectAll").checked = downloadable.length > 0 && state.selected.size === downloadable.length;
  $("downloadButton").disabled = !state.selectedComic || state.selected.size === 0;
  $("downloadTarget").textContent = `下载目录 / ${state.downloadCategory || state.detail?.tags?.[0] || "未分类"} / ${state.selectedComic?.title || "—"}`;
  if (!state.chapters.length) {
    root.className = "chapter-list empty-state";
    root.textContent = state.selectedComic ? "没有找到章节，检查列表路径或站点结构" : "从漫画详情页进入章节下载";
    return;
  }
  root.className = "chapter-list";
  state.chapters.forEach((chapter) => {
    const label = document.createElement("label");
    label.className = `chapter-row ${chapter.downloaded ? "downloaded" : ""}`;
    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.disabled = Boolean(chapter.downloaded);
    checkbox.checked = state.selected.has(chapter.url);
    checkbox.onchange = () => {
      if (checkbox.checked) state.selected.add(chapter.url);
      else state.selected.delete(chapter.url);
      renderChapters();
    };
    const order = document.createElement("small");
    order.className = "chapter-order";
    order.textContent = `序号 ${String(chapter.order).padStart(2, "0")}`;
    const title = document.createElement("span");
    title.textContent = chapter.title;
    const status = document.createElement("small");
    status.className = `chapter-status ${chapter.downloaded ? "downloaded" : "pending"}`;
    status.textContent = chapter.downloaded ? "已下载" : "待下载";
    label.append(checkbox, order, title, status);
    root.append(label);
  });
}

async function startDownload() {
  if (!state.selectedComic) return;
  const button = $("downloadButton");
  setLoading(button, true, "已加入任务…");
  try {
    const result = await api("/api/download", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ comicName: state.selectedComic.title, comicUrl: state.selectedComic.url, coverUrl: state.selectedComic.coverUrl || state.detail?.coverUrl || "", category: state.downloadCategory || state.detail?.tags?.[0] || "", chapters: state.chapters.filter((chapter) => state.selected.has(chapter.url) && !chapter.downloaded) }) });
    if (result.status === "skipped") showNotice(result.message || "所选章节都已下载");
    await loadJobs();
  } catch (error) {
    showNotice(error.message);
  } finally {
    setTimeout(() => setLoading(button, false), 600);
  }
}

function openLogin() {
  const dialog = $("loginDialog");
  if (typeof dialog.showModal === "function" && !dialog.open) dialog.showModal();
  else dialog.open = true;
  $("loginUsername").value = state.auth.username || $("loginUsername").value;
  setTimeout(() => $("loginUsername").focus(), 0);
}

async function submitLogin(event) {
  event.preventDefault();
  const status = $("loginStatus");
  status.textContent = "登录中…";
  try {
    await api("/api/auth/login", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ username: $("loginUsername").value.trim(), password: $("loginPassword").value }) });
    $("loginPassword").value = "";
    $("loginDialog").close();
    status.textContent = "";
    await loadAuth();
    await syncLibrary(true);
  } catch (error) {
    status.textContent = error.message;
  }
}

async function logout() {
  try {
    await api("/api/auth/logout", { method: "POST" });
    state.comics = [];
    state.selectedComic = null;
    state.detail = null;
    state.chapters = [];
    state.selected.clear();
    await loadAuth();
    await syncLibrary(true);
    showNotice("");
  } catch (error) {
    showNotice(error.message);
  }
}

async function loadJobs() {
  try {
    const data = await api("/api/jobs");
    state.jobs = data.jobs || [];
    const active = state.jobs.filter((job) => job.status === "running" || job.status === "queued").length;
    $("jobCount").textContent = active;
    $("heroJobCount").textContent = active;
    $("clearCompletedButton").disabled = !state.auth.authenticated || !state.jobs.some((job) => job.status === "completed");
    renderJobs();
    refreshDownloaded();
  } catch (_) {}
}

function formatBytes(bytes) {
  if (!bytes) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let value = bytes;
  let index = 0;
  while (value >= 1024 && index < units.length - 1) { value /= 1024; index += 1; }
  return `${value >= 10 || index === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[index]}`;
}

function formatSpeed(bytes) { return bytes ? `${formatBytes(bytes)}/s` : "等待数据…"; }

async function clearCompletedJobs() {
  const button = $("clearCompletedButton");
  setLoading(button, true, "清除中…");
  try {
    const result = await api("/api/jobs/clear", { method: "POST" });
    showNotice(result.removed ? `已清除 ${result.removed} 个已完成任务` : "没有可清除的已完成任务");
    await loadJobs();
  } catch (error) {
    showNotice(error.message);
  } finally {
    setLoading(button, false);
    button.disabled = !state.auth.authenticated || !state.jobs.some((job) => job.status === "completed");
  }
}

async function retryChapter(jobID, chapterID, button) {
  setLoading(button, true, "排队中…");
  try {
    await api("/api/jobs/retry", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ jobId: jobID, chapterId: chapterID }) });
    showNotice("已加入队列，重试不会插队");
    await loadJobs();
  } catch (error) {
    showNotice(error.message);
    setLoading(button, false);
  }
}

function createJobCover(url, alt, className, emptyText) {
  const frame = document.createElement("div");
  frame.className = `job-cover ${className || ""}`;
  const showPlaceholder = () => {
    frame.replaceChildren();
    frame.classList.add("placeholder");
    frame.textContent = emptyText;
  };
  if (!url) {
    showPlaceholder();
    return frame;
  }
  const image = document.createElement("img");
  image.loading = "lazy";
  image.decoding = "async";
  image.alt = alt;
  image.src = "/api/cover?url=" + encodeURIComponent(url);
  image.onerror = showPlaceholder;
  frame.append(image);
  return frame;
}

function renderJobs() {
  const root = $("jobs");
  const openJobs = new Set([...root.querySelectorAll("details[data-job-id][open]")].map((item) => item.dataset.jobId));
  root.replaceChildren();
  if (!state.jobs.length) {
    root.className = "jobs-list empty-state";
    root.textContent = "还没有下载任务";
    return;
  }
  root.className = "jobs-list";
  state.jobs.forEach((job) => {
    const item = document.createElement("details");
    item.className = "job-group";
    item.dataset.jobId = job.id;
    const active = job.status === "running" || job.status === "queued";
    item.open = state.openJobs.has(job.id) || openJobs.has(job.id) || (active && !state.closedJobs.has(job.id));
    item.ontoggle = () => {
      if (item.open) {
        state.openJobs.add(job.id);
        state.closedJobs.delete(job.id);
      } else {
        state.openJobs.delete(job.id);
        state.closedJobs.add(job.id);
      }
    };

    const summary = document.createElement("summary");
    summary.className = "job-summary";
    const summaryMain = document.createElement("div");
    summaryMain.className = "job-summary-main";
    summaryMain.append(createJobCover(job.coverUrl, job.comic, "job-main-cover", "暂无主封面"));
    const summaryInfo = document.createElement("div");
    summaryInfo.className = "job-summary-info";
    const titleRow = document.createElement("div");
    titleRow.className = "job-title-row";
    const name = document.createElement("strong");
    name.textContent = job.comic;
    const status = document.createElement("span");
    status.className = `job-status ${job.status}`;
    status.textContent = jobStatus(job);
    titleRow.append(name, status);
    const summaryMeta = document.createElement("span");
    summaryMeta.className = "job-summary-meta";
    summaryMeta.textContent = `${job.done}/${job.total} 卷 · ${job.files?.length || 0} 个 EPUB · ${formatSpeed(job.speedBps)}`;
    summaryInfo.append(titleRow, summaryMeta);
    summaryMain.append(summaryInfo);
    const toggle = document.createElement("span");
    toggle.className = "job-toggle";
    toggle.innerHTML = '<span class="job-toggle-icon">⌄</span><span>展开</span>';
    summary.append(summaryMain, toggle);
    item.append(summary);

    const body = document.createElement("div");
    body.className = "job-body";
    const overview = document.createElement("div");
    overview.className = "job-overview";
    const progress = document.createElement("div");
    progress.className = "progress";
    const fill = document.createElement("i");
    const fraction = job.bytesTotal > 0 ? job.bytesDone / job.bytesTotal : (job.total ? job.done / job.total : 0);
    fill.style.width = `${Math.min(100, Math.round(fraction * 100))}%`;
    progress.append(fill);
    const meta = document.createElement("div");
    meta.className = "job-meta";
    const chapterMeta = document.createElement("span");
    chapterMeta.textContent = `${job.done}/${job.total} 卷已结束`;
    const byteMeta = document.createElement("span");
    byteMeta.textContent = job.bytesTotal > 0 ? `${formatBytes(job.bytesDone)} / ${formatBytes(job.bytesTotal)} · ${formatSpeed(job.speedBps)}` : formatSpeed(job.speedBps);
    meta.append(chapterMeta, byteMeta);
    overview.append(progress, meta);
    body.append(overview);

    const chapters = document.createElement("div");
    chapters.className = "job-chapters";
    (job.chapters || []).forEach((chapter) => {
      const row = document.createElement("div");
      row.className = "job-chapter";
      row.append(createJobCover(chapter.coverUrl, chapter.title, "job-volume-cover", "该卷暂无封面"));
      const content = document.createElement("div");
      content.className = "job-chapter-content";
      const info = document.createElement("div");
      info.className = "job-chapter-info";
      const title = document.createElement("strong");
      title.textContent = chapter.title;
      const chapterStatus = document.createElement("span");
      chapterStatus.className = `chapter-job-status ${chapter.status}`;
      chapterStatus.textContent = jobChapterStatus(chapter);
      info.append(title, chapterStatus);
      const chapterProgress = document.createElement("div");
      chapterProgress.className = "progress chapter-progress";
      const chapterFill = document.createElement("i");
      const chapterFraction = chapter.total > 0 ? chapter.done / chapter.total : (chapter.status === "completed" ? 1 : 0);
      chapterFill.style.width = `${Math.min(100, Math.round(chapterFraction * 100))}%`;
      chapterProgress.append(chapterFill);
      const footer = document.createElement("div");
      footer.className = "job-chapter-footer";
      const chapterMeta = document.createElement("span");
      chapterMeta.className = "job-chapter-meta";
      chapterMeta.textContent = chapter.total > 0 ? `${formatBytes(chapter.done)} / ${formatBytes(chapter.total)} · ${formatSpeed(chapter.speedBps)}` : formatSpeed(chapter.speedBps);
      footer.append(chapterMeta);
      if (chapter.status === "failed") {
        const retry = document.createElement("button");
        retry.className = "button retry-button";
        retry.type = "button";
        retry.textContent = "重试";
        retry.onclick = (event) => { event.preventDefault(); event.stopPropagation(); retryChapter(job.id, chapter.id, retry); };
        footer.append(retry);
        const failure = document.createElement("div");
        failure.className = "failure";
        failure.textContent = chapter.error || "下载失败";
        content.append(info, chapterProgress, footer, failure);
      } else {
        content.append(info, chapterProgress, footer);
      }
      row.append(content);
      chapters.append(row);
    });
    body.append(chapters);
    item.append(body);
    root.append(item);
  });
}

function jobStatus(job) { return { queued: "排队中", running: "下载中", completed: "已完成", completed_with_errors: "部分失败", failed: "失败" }[job.status] || job.status; }
function jobChapterStatus(chapter) { return { queued: "排队中", running: "下载中", completed: "已完成", failed: "失败" }[chapter.status] || chapter.status; }

$("syncButton").onclick = () => syncLibrary();
$("jobNavButton").onclick = openDownloadView;
$("searchButton").onclick = () => { state.search = $("searchInput").value.trim(); syncLibrary(true); };
$("searchInput").onkeydown = (event) => { if (event.key === "Enter") { event.preventDefault(); $("searchButton").click(); } };
$("categorySelect").onchange = () => { state.category = $("categorySelect").value; syncLibrary(true); };
$("selectAll").onchange = () => { state.selected = new Set($("selectAll").checked ? state.chapters.filter((chapter) => !chapter.downloaded).map((chapter) => chapter.url) : []); renderChapters(); };
$("downloadButton").onclick = startDownload;
$("clearCompletedButton").onclick = clearCompletedJobs;
$("chaptersButton").onclick = openDownloadView;
$("backLibraryButton").onclick = () => switchView("library", "back");
$("backDetailButton").onclick = () => switchView(state.selectedComic ? "detail" : "library", "back");
$("loginButton").onclick = openLogin;
$("closeLoginButton").onclick = () => $("loginDialog").close();
$("loginForm").onsubmit = submitLogin;
$("logoutButton").onclick = logout;

renderCategories();
renderDetailShell();
renderChapters();
Promise.all([loadAuth(), loadJobs()]).then(() => syncLibrary(true)).catch((error) => showNotice(error.message));
setInterval(loadJobs, 2000);
