async function api(path, options) {
  const response = await fetch(path, options);
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || `请求失败（${response.status}）`);
  return data;
}

const $ = (id) => document.getElementById(id);
const files = { path: "", root: "", entries: [] };

function notice(message) {
  $("adminNotice").textContent = message || "";
  $("adminNotice").classList.toggle("hidden", !message);
}

async function load() {
  const settings = await api("/api/settings");
  $("upstreamUrl").value = settings.upstreamUrl || "";
  $("loginPath").value = settings.loginPath || "/login.php";
  $("libraryPath").value = settings.libraryPath || "/";
  $("nasDir").value = settings.nasDir || "";
  $("proxyUrl").value = settings.proxyUrl || "";
  $("proxyEnabled").checked = Boolean(settings.proxyEnabled);
  $("workers").value = settings.workers || 4;
  $("username").value = settings.username || "";
  $("adminAuth").textContent = settings.authenticated ? `已登录：${settings.username || "账号"}` : "未登录";
  $("adminAuth").className = `badge ${settings.authenticated ? "success" : "muted"}`;
  await loadFiles();
}

async function loadFiles() {
  try {
    const data = await api(`/api/files?path=${encodeURIComponent(files.path)}`);
    files.path = data.path || "";
    files.entries = data.entries || [];
    renderFiles();
  } catch (error) {
    $("fileList").className = "file-list empty-state";
    $("fileList").textContent = error.message;
  }
}

function formatBytes(bytes) {
  if (!bytes) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let value = bytes;
  let index = 0;
  while (value >= 1024 && index < units.length - 1) { value /= 1024; index += 1; }
  return `${value >= 10 || index === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[index]}`;
}

function renderFiles() {
  $("fileRoot").textContent = $("nasDir").value || "NAS 根目录";
  $("filePath").textContent = files.path ? `/${files.path}` : "/";
  $("fileUp").disabled = !files.path;
  const root = $("fileList");
  root.replaceChildren();
  if (!files.entries.length) {
    root.className = "file-list empty-state";
    root.textContent = "目录为空";
    return;
  }
  root.className = "file-list";
  files.entries.forEach((entry) => {
    const row = document.createElement("div");
    row.className = "file-row";
    const name = entry.isDir ? document.createElement("button") : document.createElement("span");
    name.className = "file-name";
    name.textContent = `${entry.isDir ? "▸" : "•"} ${entry.name}`;
    if (entry.isDir) {
      name.type = "button";
      name.onclick = () => { files.path = entry.path; loadFiles(); };
    }
    const meta = document.createElement("span");
    meta.className = "file-meta";
    meta.textContent = entry.symlink ? "链接" : `${entry.isDir ? "目录" : formatBytes(entry.size)} · ${new Date(entry.modTime).toLocaleString()}`;
    const actions = document.createElement("div");
    actions.className = "file-actions";
    if (!entry.symlink) {
      const rename = document.createElement("button");
      rename.className = "button";
      rename.type = "button";
      rename.textContent = "重命名";
      rename.onclick = () => renameEntry(entry);
      const remove = document.createElement("button");
      remove.className = "button danger";
      remove.type = "button";
      remove.textContent = "删除";
      remove.onclick = () => deleteEntry(entry);
      actions.append(rename, remove);
    }
    row.append(name, meta, actions);
    root.append(row);
  });
}

async function renameEntry(entry) {
  const newName = window.prompt("输入新的文件名", entry.name);
  if (!newName || newName.trim() === entry.name) return;
  try {
    await api("/api/files/rename", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path: entry.path, newName: newName.trim() }) });
    await loadFiles();
  } catch (error) {
    notice(error.message);
  }
}

async function deleteEntry(entry) {
  if (!window.confirm(`确认删除“${entry.name}”？非空目录不会递归删除。`)) return;
  try {
    await api(`/api/files?path=${encodeURIComponent(entry.path)}`, { method: "DELETE" });
    await loadFiles();
  } catch (error) {
    notice(error.message);
  }
}

$("settingsForm").onsubmit = async (event) => {
  event.preventDefault();
  const status = $("settingsStatus");
  status.textContent = "保存中…";
  try {
    const username = $("username").value.trim();
    const password = $("password").value;
    await api("/api/settings", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ upstreamUrl: $("upstreamUrl").value.trim(), loginPath: $("loginPath").value.trim(), libraryPath: $("libraryPath").value.trim(), nasDir: $("nasDir").value.trim(), proxyUrl: $("proxyUrl").value.trim(), proxyEnabled: $("proxyEnabled").checked, workers: Number($("workers").value), username }) });
    if (password) await api("/api/auth/login", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ username, password }) });
    $("password").value = "";
    files.path = "";
    await load();
    status.textContent = "已保存";
  } catch (error) {
    status.textContent = error.message;
  }
};

$("fileUp").onclick = () => { files.path = files.path.split("/").slice(0, -1).join("/"); loadFiles(); };
$("fileRefresh").onclick = loadFiles;
$("proxyTest").onclick = async () => {
  const button = $("proxyTest");
  const status = $("proxyStatus");
  button.disabled = true;
  status.textContent = "测试中…";
  try {
    const result = await api("/api/proxy/test", { method: "POST" });
    status.textContent = result.ok ? `${result.mode}已连通 Google · HTTP ${result.status} · ${result.elapsedMs} ms` : `${result.mode}未连通：${result.error || "未知错误"}`;
  } catch (error) {
    status.textContent = error.message;
  } finally {
    button.disabled = false;
  }
};
load().catch((error) => notice(error.message));
