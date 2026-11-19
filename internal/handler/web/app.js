const $ = (id) => document.getElementById(id);

const state = {
  token: sessionStorage.getItem("netdisk-token") || "",
  refreshToken: sessionStorage.getItem("netdisk-refresh-token") || "",
  user: null,
  stats: null,
  policy: null,
  authMode: "login",
  view: "files",
  trail: [], // [{id, name}]
  searchQuery: "",
  filterType: "all",
  sort: "name",
  order: "asc",
  limit: 30,
  offset: 0,
  total: 0,
};

// ---------------- Toast & Formatters ----------------
function toast(msg, kind = "") {
  const el = document.createElement("div");
  el.className = "toast" + (kind ? " " + kind : "");
  el.textContent = msg;
  $("toast-stack").append(el);
  setTimeout(() => el.remove(), 4000);
}

function fmtBytes(n) {
  if (!n || n <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return (i === 0 ? v : v.toFixed(v >= 100 ? 0 : 1)) + " " + units[i];
}

function fmtDate(iso) {
  if (!iso) return "—";
  const d = new Date(iso);
  return d.toLocaleString("zh-CN", { hour12: false });
}

function fileIcon(name, isDir) {
  if (isDir) return "📁";
  const ext = (name.split(".").pop() || "").toLowerCase();
  if (["png", "jpg", "jpeg", "gif", "webp", "bmp"].includes(ext)) return "🖼️";
  if (["mp4", "webm", "mov", "mkv"].includes(ext)) return "🎬";
  if (["mp3", "wav", "ogg", "flac"].includes(ext)) return "🎵";
  if (ext === "pdf") return "📕";
  if (["zip", "tar", "gz", "7z", "rar"].includes(ext)) return "🗜️";
  if (["txt", "md", "json", "go", "js", "ts", "css", "py", "sql", "yml", "yaml", "csv", "log"].includes(ext)) return "📄";
  return "📦";
}

function previewKind(name) {
  const ext = (name.split(".").pop() || "").toLowerCase();
  if (["png", "jpg", "jpeg", "gif", "webp"].includes(ext)) return "image";
  if (["mp4", "webm"].includes(ext)) return "video";
  if (["mp3", "wav", "ogg"].includes(ext)) return "audio";
  if (ext === "pdf") return "pdf";
  if (["txt", "md", "markdown", "json", "csv", "log", "yml", "yaml", "toml", "xml", "go", "js", "ts", "css", "py", "rs", "c", "cpp", "h", "java", "sh", "sql", "ini"].includes(ext)) return "text";
  return null;
}

async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    toast("已复制到剪贴板", "ok");
  } catch {
    const ta = document.createElement("textarea");
    ta.value = text;
    document.body.append(ta);
    ta.select();
    document.execCommand("copy");
    ta.remove();
    toast("已复制到剪贴板", "ok");
  }
}

// ---------------- API Client with Auto-Refresh ----------------
let refreshingPromise = null;

function saveSession(token, refreshToken) {
  state.token = token || "";
  state.refreshToken = refreshToken || "";
  if (state.token) sessionStorage.setItem("netdisk-token", state.token);
  else sessionStorage.removeItem("netdisk-token");
  if (state.refreshToken) sessionStorage.setItem("netdisk-refresh-token", state.refreshToken);
  else sessionStorage.removeItem("netdisk-refresh-token");
}

async function tryRefresh() {
  if (!state.refreshToken) return false;
  if (!refreshingPromise) {
    refreshingPromise = (async () => {
      try {
        const res = await fetch("/api/v1/auth/refresh", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ refresh_token: state.refreshToken }),
        });
        if (!res.ok) return false;
        const data = await res.json();
        saveSession(data.token, data.refresh_token);
        state.user = data.user;
        return true;
      } catch {
        return false;
      } finally {
        refreshingPromise = null;
      }
    })();
  }
  return refreshingPromise;
}

async function api(path, method = "GET", body, opts = {}) {
  const raw = body instanceof Blob;
  const doFetch = () =>
    fetch("/api/v1" + path, {
      method,
      headers: {
        ...(state.token ? { Authorization: "Bearer " + state.token } : {}),
        ...(body !== undefined && !raw ? { "Content-Type": "application/json" } : {}),
      },
      body: body === undefined ? undefined : raw ? body : JSON.stringify(body),
    });

  let r = await doFetch();
  if (r.status === 401 && !opts.noRetry && state.refreshToken && !path.startsWith("/auth/")) {
    const ok = await tryRefresh();
    if (ok) r = await doFetch();
  }
  if (!r.ok) {
    let msg = "请求失败 (" + r.status + ")";
    let code = "";
    try {
      const e = await r.json();
      msg = e.error?.message || msg;
      code = e.error?.code || "";
    } catch {}
    const err = new Error(msg);
    err.status = r.status;
    err.code = code;
    throw err;
  }
  return r.status === 204 ? null : r.json();
}

// ---------------- Modal System ----------------
function closeModal() {
  $("modal-root").hidden = true;
  $("modal-root").replaceChildren();
}

function openModal({ title, bodyEl, footerButtons = [], wide = false }) {
  const root = $("modal-root");
  root.replaceChildren();
  const box = document.createElement("div");
  box.className = "modal" + (wide ? " wide" : "");

  const header = document.createElement("div");
  header.className = "modal-header";
  const h3 = document.createElement("h3");
  h3.textContent = title;
  const x = document.createElement("button");
  x.type = "button";
  x.className = "ghost sm";
  x.textContent = "✕";
  x.onclick = closeModal;
  header.append(h3, x);

  const body = document.createElement("div");
  body.className = "modal-body";
  body.append(bodyEl);

  box.append(header, body);
  if (footerButtons.length > 0) {
    const footer = document.createElement("div");
    footer.className = "modal-footer";
    for (const btn of footerButtons) footer.append(btn);
    box.append(footer);
  }
  root.append(box);
  root.hidden = false;
  root.onclick = (e) => {
    if (e.target === root) closeModal();
  };
}

function makeBtn(label, cls, onClick) {
  const b = document.createElement("button");
  b.type = "button";
  b.textContent = label;
  if (cls) b.className = cls;
  b.onclick = onClick;
  return b;
}

// ---------------- Auth View ----------------
function setAuthMode(mode) {
  state.authMode = mode;
  $("tab-login").classList.toggle("active", mode === "login");
  $("tab-register").classList.toggle("active", mode === "register");
  $("register-extra").hidden = mode === "login";
  $("auth-submit").textContent = mode === "login" ? "登录网盘" : "创建账号";
}
$("tab-login").onclick = () => setAuthMode("login");
$("tab-register").onclick = () => setAuthMode("register");

$("auth-form").onsubmit = async (e) => {
  e.preventDefault();
  const username = $("auth-username").value.trim();
  const password = $("auth-password").value;
  $("auth-submit").disabled = true;
  try {
    if (state.authMode === "register") {
      await api("/auth/register", "POST", {
        username,
        password,
        nickname: $("auth-nickname").value.trim(),
        email: $("auth-email").value.trim(),
      });
      toast("注册成功，正在自动登录…", "ok");
    }
    const data = await api("/auth/login", "POST", { username, password });
    saveSession(data.token, data.refresh_token);
    $("auth-password").value = "";
    await enterApp();
    toast("欢迎回来，" + (state.user.nickname || state.user.username), "ok");
  } catch (err) {
    toast(err.message, "err");
  } finally {
    $("auth-submit").disabled = false;
  }
};

// ---------------- Shell & Navigation ----------------
function switchView(view) {
  state.view = view;
  document.querySelectorAll(".nav-item[data-view]").forEach((el) => {
    el.classList.toggle("active", el.dataset.view === view);
  });
  ["files", "trash", "shares", "storage"].forEach((v) => {
    $("view-" + v).hidden = v !== view;
  });
  if (view === "files") loadFiles();
  else if (view === "trash") loadTrash();
  else if (view === "shares") loadShares();
  else if (view === "storage") loadStorageView();
}

document.querySelectorAll(".nav-item[data-view]").forEach((btn) => {
  btn.onclick = () => switchView(btn.dataset.view);
});

async function refreshStatsAndPolicy() {
  try {
    const [statsData, policyData] = await Promise.all([
      api("/auth/stats"),
      api("/storage/policy"),
    ]);
    state.stats = statsData.stats;
    state.policy = policyData.policy;
    renderSidebarStats();
  } catch {}
}

function renderSidebarStats() {
  const s = state.stats;
  if (!s) return;
  $("pill-files").textContent = String(s.file_count + s.dir_count);
  $("pill-trash").textContent = String(s.trash_count);
  $("pill-shares").textContent = String(s.share_count);
  if (state.policy) $("pill-backend").textContent = state.policy.default_backend;

  const pct = s.quota_bytes > 0 ? Math.min(100, Math.round((s.used_bytes / s.quota_bytes) * 100)) : 0;
  $("quota-percent").textContent = s.quota_bytes > 0 ? pct + "%" : "不限";
  const fill = $("quota-fill");
  fill.style.width = (s.quota_bytes > 0 ? pct : 0) + "%";
  fill.className = "quota-fill" + (pct >= 90 ? " danger" : pct >= 75 ? " warn" : "");
  $("quota-meta").textContent =
    s.quota_bytes > 0
      ? `已用 ${fmtBytes(s.used_bytes)} / ${fmtBytes(s.quota_bytes)}（回收站 ${fmtBytes(s.trash_bytes)}）`
      : `已用 ${fmtBytes(s.used_bytes)}（回收站 ${fmtBytes(s.trash_bytes)}）`;

  if (s.dedup_saved_bytes > 0) {
    $("dedup-chip").hidden = false;
    $("dedup-chip").textContent = "⚡ 去重节省 " + fmtBytes(s.dedup_saved_bytes);
  } else {
    $("dedup-chip").hidden = true;
  }
}

async function enterApp() {
  const me = await api("/auth/me");
  state.user = me.user;
  $("auth-view").hidden = true;
  $("app-view").hidden = false;
  $("user-name").textContent = state.user.nickname || state.user.username;
  $("user-sub").textContent = state.user.email || "@" + state.user.username;
  await refreshStatsAndPolicy();
  await loadFiles();
}

// ---------------- Files View ----------------
function currentParentID() {
  return state.trail.length ? state.trail[state.trail.length - 1].id : null;
}

function renderBreadcrumbs() {
  const wrap = $("breadcrumbs");
  wrap.replaceChildren();

  if (state.searchQuery) {
    const span = document.createElement("span");
    span.innerHTML = `🔍 全盘搜索：<strong></strong>`;
    span.querySelector("strong").textContent = state.searchQuery;
    wrap.append(span);
    return;
  }

  const rootBtn = makeBtn("根目录", "", () => {
    state.trail = [];
    state.offset = 0;
    loadFiles();
  });
  wrap.append(rootBtn);

  state.trail.forEach((crumb, idx) => {
    const sep = document.createElement("span");
    sep.className = "sep";
    sep.textContent = "/";
    const b = makeBtn(crumb.name, "", () => {
      state.trail = state.trail.slice(0, idx + 1);
      state.offset = 0;
      loadFiles();
    });
    wrap.append(sep, b);
  });
}

async function loadFiles() {
  renderBreadcrumbs();
  const params = new URLSearchParams();
  if (state.searchQuery) {
    params.set("q", state.searchQuery);
  } else {
    params.set("parent_id", currentParentID() || "root");
  }
  if (state.filterType !== "all") params.set("type", state.filterType);
  params.set("sort", state.sort);
  params.set("order", state.order);
  params.set("limit", String(state.limit));
  params.set("offset", String(state.offset));

  try {
    const data = await api("/files/?" + params.toString());
    state.total = data.total ?? data.files.length;
    renderFileRows(data.files);
    renderPager(data);
  } catch (err) {
    toast("加载文件失败：" + err.message, "err");
  }
}

function renderPager(data) {
  const pager = $("files-pager");
  if (!data.total || data.total <= state.limit) {
    pager.hidden = true;
    return;
  }
  pager.hidden = false;
  const page = Math.floor(state.offset / state.limit) + 1;
  const totalPages = Math.ceil(data.total / state.limit);
  $("pager-info").textContent = `第 ${page} / ${totalPages} 页 · 共 ${data.total} 项`;
  $("btn-prev-page").disabled = state.offset <= 0;
  $("btn-next-page").disabled = !data.has_more;
}

function renderFileRows(files) {
  const tbody = $("file-rows");
  tbody.replaceChildren();
  const empty = $("files-empty");
  if (!files.length) {
    empty.hidden = false;
    $("files-empty-text").textContent = state.searchQuery
      ? `未找到包含「${state.searchQuery}」的文件或文件夹`
      : "当前目录为空，拖拽或选择文件开始上传";
    return;
  }
  empty.hidden = true;

  const s3Ready = !!state.policy?.s3_configured;

  for (const n of files) {
    const tr = document.createElement("tr");

    // Name column
    const tdName = document.createElement("td");
    const cell = document.createElement("div");
    cell.className = "file-cell";
    const icon = document.createElement("span");
    icon.className = "file-icon";
    icon.textContent = fileIcon(n.name, n.is_dir);
    const link = document.createElement("span");
    link.className = "file-link";
    link.textContent = n.name;
    const pk = !n.is_dir ? previewKind(n.name) : null;
    link.onclick = () => {
      if (n.is_dir) {
        if (state.searchQuery) {
          jumpToFolder(n.id);
        } else {
          state.trail.push({ id: n.id, name: n.name });
          state.offset = 0;
          loadFiles();
        }
      } else if (pk) {
        openPreviewModal(n, pk);
      } else {
        location.href = "/api/v1/files/" + n.id + "/download";
      }
    };
    cell.append(icon, link);
    tdName.append(cell);

    // Size column
    const tdSize = document.createElement("td");
    tdSize.className = "mono";
    tdSize.textContent = n.is_dir ? "—" : fmtBytes(n.size);
    if (!n.is_dir) tdSize.title = n.size.toLocaleString() + " 字节";

    // Storage / heat column
    const tdStore = document.createElement("td");
    if (n.is_dir) {
      tdStore.innerHTML = '<span class="mono">目录</span>';
    } else {
      const b = document.createElement("span");
      const backend = n.backend || "local";
      b.className = "badge " + (backend === "s3" ? "s3" : "local");
      b.textContent = backend.toUpperCase();
      tdStore.append(b);
      if (n.download_count > 0) {
        const heat = document.createElement("span");
        heat.className = "mono";
        heat.style.marginLeft = "6px";
        heat.textContent = `↓${n.download_count}`;
        tdStore.append(heat);
      }
    }

    // Updated time
    const tdTime = document.createElement("td");
    tdTime.className = "mono";
    tdTime.textContent = fmtDate(n.updated_at);

    // Actions
    const tdOps = document.createElement("td");
    const ops = document.createElement("div");
    ops.className = "ops";

    if (n.is_dir) {
      ops.append(
        makeBtn("打包 ZIP", "secondary sm", () => {
          location.href = "/api/v1/files/" + n.id + "/zip";
        }),
      );
    } else {
      if (pk) {
        ops.append(makeBtn("预览", "secondary sm", () => openPreviewModal(n, pk)));
      }
      ops.append(
        makeBtn("下载", "secondary sm", () => {
          location.href = "/api/v1/files/" + n.id + "/download";
        }),
      );
      if (s3Ready) {
        if (n.backend === "s3") {
          ops.append(
            makeBtn("S3 直链", "secondary sm", async () => {
              try {
                const res = await api("/files/" + n.id + "/direct-url");
                window.open(res.url, "_blank", "noopener");
              } catch (e) {
                toast(e.message, "err");
              }
            }),
            makeBtn("迁回本地", "secondary sm", () => migrateNode(n, "local")),
          );
        } else {
          ops.append(makeBtn("迁至 S3", "secondary sm", () => migrateNode(n, "s3")));
        }
      }
    }

    ops.append(
      makeBtn("分享", "secondary sm", () => openCreateShareModal(n)),
      makeBtn("改名", "secondary sm", () => openRenameModal(n)),
      makeBtn("移动", "secondary sm", () => openMoveModal(n)),
      makeBtn("删除", "danger secondary sm", () => moveToTrash(n)),
    );

    tdOps.append(ops);
    tr.append(tdName, tdSize, tdStore, tdTime, tdOps);
    tbody.append(tr);
  }
}

async function jumpToFolder(folderID) {
  try {
    const res = await api("/files/" + folderID + "/ancestors");
    state.searchQuery = "";
    $("search-input").value = "";
    $("btn-clear-search").hidden = true;
    state.trail = res.ancestors.map((a) => ({ id: a.id, name: a.name }));
    state.offset = 0;
    loadFiles();
  } catch (e) {
    toast(e.message, "err");
  }
}

async function migrateNode(n, target) {
  toast(`正在将「${n.name}」迁移到 ${target.toUpperCase()}…`);
  try {
    await api("/files/" + n.id + "/migrate", "POST", { backend: target });
    toast(`「${n.name}」已迁移至 ${target.toUpperCase()}（校验通过）`, "ok");
    await Promise.all([loadFiles(), refreshStatsAndPolicy()]);
  } catch (e) {
    toast("迁移失败：" + e.message, "err");
  }
}

async function moveToTrash(n) {
  try {
    await api("/files/" + n.id, "DELETE");
    toast(`「${n.name}」已移入回收站，可随时恢复`, "ok");
    await Promise.all([loadFiles(), refreshStatsAndPolicy()]);
  } catch (e) {
    toast(e.message, "err");
  }
}

// ---------------- Upload Queue (Instant SHA-256 + Chunked Resumable) ----------------
async function sha256Hex(file) {
  if (!window.crypto?.subtle || file.size > 64 * 1024 * 1024) return null;
  const buf = await file.arrayBuffer();
  const digest = await crypto.subtle.digest("SHA-256", buf);
  return Array.from(new Uint8Array(digest))
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}

async function handleUploadFiles(fileList) {
  const files = Array.from(fileList || []);
  if (!files.length) return;
  const queueEl = $("upload-queue");
  queueEl.hidden = false;

  for (const file of files) {
    await uploadSingleFile(file, queueEl);
  }
  await Promise.all([loadFiles(), refreshStatsAndPolicy()]);
}

async function uploadSingleFile(file, queueEl) {
  const row = document.createElement("div");
  row.className = "upload-item";
  const head = document.createElement("div");
  head.className = "upload-item-head";
  const title = document.createElement("strong");
  title.textContent = `${file.name} (${fmtBytes(file.size)})`;
  const info = document.createElement("span");
  info.className = "mono";
  info.textContent = "准备中…";
  const prog = document.createElement("progress");
  prog.max = 100;
  prog.value = 0;
  head.append(title, info);
  row.append(head, prog);
  queueEl.prepend(row);

  const targetParent = currentParentID();
  try {
    // 1. 尝试秒传（<= 64 MiB 且支持 WebCrypto）
    let hash = null;
    if (window.crypto?.subtle && file.size <= 64 * 1024 * 1024) {
      info.textContent = "计算 SHA-256 校验秒传…";
      hash = await sha256Hex(file);
      if (hash) {
        try {
          await api("/files/instant", "POST", {
            name: file.name,
            parent_id: targetParent || "",
            hash,
            size: file.size,
          });
          prog.value = 100;
          info.textContent = "⚡ 秒传成功（零字节传输）";
          toast(`「${file.name}」秒传成功`, "ok");
          setTimeout(() => row.remove(), 4000);
          return;
        } catch (err) {
          if (err.status !== 404) throw err;
        }
      }
    }

    // 2. 分片断点续传
    const chunkSize = 4 * 1024 * 1024;
    const key = `upload:${state.user.id}:${targetParent}:${file.name}:${file.size}:${file.lastModified}`;
    let sid = sessionStorage.getItem(key);
    let sessState = null;

    if (sid) {
      try {
        sessState = await api("/uploads/" + sid);
        if (sessState.session.status !== "pending") sid = null;
      } catch {
        sid = null;
      }
    }
    if (!sid) {
      const payload = {
        name: file.name,
        parent_id: targetParent || "",
        total_size: file.size,
        chunk_size: chunkSize,
      };
      if (hash) payload.content_hash = hash;
      const created = await api("/uploads/", "POST", payload);
      sid = created.session.id;
      sessionStorage.setItem(key, sid);
      sessState = await api("/uploads/" + sid);
    }

    const totalParts = sessState.session.part_count;
    let doneParts = sessState.received_parts.length;
    prog.value = Math.round((doneParts / totalParts) * 100);
    const startTs = performance.now();
    let uploadedBytes = 0;

    for (const partNo of sessState.missing_parts) {
      const start = partNo * sessState.session.chunk_size;
      const end = Math.min(start + sessState.session.chunk_size, file.size);
      const blob = file.slice(start, end);
      await api(`/uploads/${sid}/parts/${partNo}`, "PUT", blob);
      doneParts++;
      uploadedBytes += blob.size;
      const elapsed = Math.max(0.05, (performance.now() - startTs) / 1000);
      const speed = fmtBytes(uploadedBytes / elapsed) + "/s";
      prog.value = Math.round((doneParts / totalParts) * 100);
      info.textContent = `分片 ${doneParts}/${totalParts} · ${speed}`;
    }

    info.textContent = "正在服务端校验并合并…";
    await api(`/uploads/${sid}/complete`, "POST");
    sessionStorage.removeItem(key);
    prog.value = 100;
    info.textContent = "✓ 上传完成";
    toast(`「${file.name}」上传完成`, "ok");
    setTimeout(() => {
      row.remove();
      if (!queueEl.children.length) queueEl.hidden = true;
    }, 3500);
  } catch (err) {
    info.textContent = "失败：" + err.message;
    toast(`上传「${file.name}」失败：${err.message}`, "err");
  }
}

// ---------------- Modals: Mkdir / Rename / Move / Preview / Share / Analytics / Settings ----------------
function openMkdirModal() {
  const wrap = document.createElement("div");
  wrap.className = "field";
  wrap.innerHTML = `<label>文件夹名称</label><input type="text" id="m-mkdir-name" placeholder="例如：学习资料" />`;
  const submit = async () => {
    const name = $("m-mkdir-name").value.trim();
    if (!name) return toast("请输入文件夹名称", "err");
    try {
      await api("/files/dirs", "POST", { name, parent_id: currentParentID() || "" });
      closeModal();
      toast("文件夹已创建", "ok");
      await Promise.all([loadFiles(), refreshStatsAndPolicy()]);
    } catch (e) {
      toast(e.message, "err");
    }
  };
  openModal({
    title: "新建文件夹",
    bodyEl: wrap,
    footerButtons: [makeBtn("取消", "secondary", closeModal), makeBtn("创建", "", submit)],
  });
  setTimeout(() => $("m-mkdir-name")?.focus(), 30);
}

function openRenameModal(node) {
  const wrap = document.createElement("div");
  wrap.className = "field";
  wrap.innerHTML = `<label>新名称</label><input type="text" id="m-rename-val" />`;
  wrap.querySelector("input").value = node.name;
  const submit = async () => {
    const name = $("m-rename-val").value.trim();
    if (!name) return toast("名称不能为空", "err");
    try {
      await api("/files/" + node.id, "PATCH", { name });
      closeModal();
      toast("重命名成功", "ok");
      await loadFiles();
    } catch (e) {
      toast(e.message, "err");
    }
  };
  openModal({
    title: "重命名",
    bodyEl: wrap,
    footerButtons: [makeBtn("取消", "secondary", closeModal), makeBtn("保存", "", submit)],
  });
  setTimeout(() => $("m-rename-val")?.select(), 30);
}

async function openMoveModal(node) {
  try {
    const data = await api("/files/folders");
    const folders = data.folders || [];
    const byParent = new Map();
    for (const f of folders) {
      const k = f.parent_id || "root";
      if (!byParent.has(k)) byParent.set(k, []);
      byParent.get(k).push(f);
    }

    let selectedParent = null; // null = root
    const wrap = document.createElement("div");
    wrap.innerHTML = `<p class="muted" style="margin-top:0;">选择「${node.name}」的目标文件夹：</p><div class="folder-picker" id="m-folder-tree"></div>`;
    const tree = wrap.querySelector("#m-folder-tree");

    const options = [];
    function addOpt(id, label, depth, disabled) {
      const div = document.createElement("div");
      div.className = "folder-option" + (id === null ? " selected" : "");
      div.style.paddingLeft = 10 + depth * 18 + "px";
      div.textContent = "📁 " + label;
      if (disabled) {
        div.style.opacity = "0.4";
        div.style.cursor = "not-allowed";
      } else {
        div.onclick = () => {
          selectedParent = id;
          options.forEach((o) => o.classList.remove("selected"));
          div.classList.add("selected");
        };
      }
      options.push(div);
      tree.append(div);
    }

    addOpt(null, "根目录", 0, false);
    function walk(pid, depth, insideSelf) {
      for (const f of byParent.get(pid) || []) {
        const isSelf = f.id === node.id;
        addOpt(f.id, f.name, depth, insideSelf || isSelf);
        walk(f.id, depth + 1, insideSelf || isSelf);
      }
    }
    walk("root", 1, false);

    openModal({
      title: "移动到文件夹",
      bodyEl: wrap,
      footerButtons: [
        makeBtn("取消", "secondary", closeModal),
        makeBtn("确认移动", "", async () => {
          try {
            await api("/files/" + node.id, "PATCH", { parent_id: selectedParent });
            closeModal();
            toast("移动成功", "ok");
            await loadFiles();
          } catch (e) {
            toast(e.message, "err");
          }
        }),
      ],
    });
  } catch (e) {
    toast("加载目录树失败：" + e.message, "err");
  }
}

async function openPreviewModal(node, kind) {
  const url = `/api/v1/files/${node.id}/download?inline=1`;
  const wrap = document.createElement("div");
  const box = document.createElement("div");
  box.className = "preview-box";
  wrap.append(box);

  if (kind === "image") {
    const img = document.createElement("img");
    img.src = url;
    img.alt = node.name;
    box.append(img);
  } else if (kind === "video") {
    const v = document.createElement("video");
    v.src = url;
    v.controls = true;
    box.append(v);
  } else if (kind === "audio") {
    const a = document.createElement("audio");
    a.src = url;
    a.controls = true;
    a.style.width = "100%";
    box.append(a);
  } else if (kind === "pdf") {
    const f = document.createElement("iframe");
    f.src = url;
    box.append(f);
  } else if (kind === "text") {
    const pre = document.createElement("pre");
    pre.textContent = "加载内容中…";
    box.append(pre);
    fetch(url, { headers: state.token ? { Authorization: "Bearer " + state.token } : {} })
      .then((r) => r.text())
      .then((txt) => {
        pre.textContent = txt.length > 200000 ? txt.slice(0, 200000) + "\n\n…（内容过长，请下载查看完整文件）" : txt;
      })
      .catch((e) => {
        pre.textContent = "加载失败：" + e.message;
      });
  }

  openModal({
    title: `预览 · ${node.name}`,
    bodyEl: wrap,
    wide: true,
    footerButtons: [
      makeBtn("下载原文件", "secondary", () => (location.href = `/api/v1/files/${node.id}/download`)),
      makeBtn("关闭", "", closeModal),
    ],
  });
}

function openCreateShareModal(node) {
  const wrap = document.createElement("div");
  wrap.innerHTML = `
    <div class="field">
      <label>分享对象</label>
      <input type="text" readonly value="${node.name.replace(/"/g, "&quot;")}" />
    </div>
    <div class="field">
      <label for="m-share-exp">有效期</label>
      <select id="m-share-exp">
        <option value="86400">24 小时</option>
        <option value="604800">7 天</option>
        <option value="2592000">30 天</option>
        <option value="0">永久有效</option>
      </select>
    </div>
    <div class="field">
      <label for="m-share-pw">提取码 / 访问密码（可选，4–72 位）</label>
      <input id="m-share-pw" type="text" placeholder="留空表示公开访问" />
    </div>
    <div class="field">
      <label for="m-share-max">最大下载次数（可选）</label>
      <input id="m-share-max" type="number" min="1" placeholder="留空表示不限次数" />
    </div>
    <div id="m-share-result" class="status-banner" hidden></div>
  `;

  const createBtn = makeBtn("生成分享链接", "", async () => {
    const payload = {
      node_id: node.id,
      expires_in_seconds: Number($("m-share-exp").value),
    };
    const pw = $("m-share-pw").value.trim();
    if (pw) payload.password = pw;
    const maxRaw = $("m-share-max").value.trim();
    if (maxRaw) payload.max_downloads = Number(maxRaw);

    try {
      const data = await api("/shares/", "POST", payload);
      const link = `${location.origin}/share.html#${data.share.token}`;
      const resBox = $("m-share-result");
      resBox.hidden = false;
      resBox.textContent = "分享链接已创建：\n" + link + (pw ? `\n提取码：${pw}` : "");
      await copyText(pw ? `${link} 提取码：${pw}` : link);
      await refreshStatsAndPolicy();
    } catch (e) {
      toast(e.message, "err");
    }
  });

  openModal({
    title: "创建分享链接",
    bodyEl: wrap,
    footerButtons: [makeBtn("关闭", "secondary", closeModal), createBtn],
  });
}

async function openShareAnalyticsModal(share) {
  try {
    const { analytics: a } = await api(`/shares/${share.id}/analytics`);
    const wrap = document.createElement("div");

    const kpis = document.createElement("div");
    kpis.className = "kpi-grid";
    kpis.innerHTML = `
      <div class="kpi"><small>总浏览次数</small><strong>${a.share.visit_count}</strong></div>
      <div class="kpi"><small>总下载次数</small><strong>${a.share.download_count}${a.share.max_downloads ? " / " + a.share.max_downloads : ""}</strong></div>
      <div class="kpi"><small>独立访客 IP</small><strong>${a.unique_ips}</strong></div>
      <div class="kpi"><small>提取码错误</small><strong>${a.failed_auths}</strong></div>
    `;
    wrap.append(kpis);

    if (a.daily_stats && a.daily_stats.length) {
      const maxVal = Math.max(1, ...a.daily_stats.map((d) => Math.max(d.views, d.downloads)));
      const chartTitle = document.createElement("div");
      chartTitle.className = "muted";
      chartTitle.style.marginBottom = "4px";
      chartTitle.textContent = "近 7 天趋势（浅蓝：浏览 / 深青：下载）";
      const chart = document.createElement("div");
      chart.className = "chart-bars";
      for (const d of a.daily_stats) {
        const col = document.createElement("div");
        col.className = "chart-col";
        col.innerHTML = `
          <div class="chart-pair">
            <div class="bar-view" style="height:${Math.max(4, Math.round((d.views / maxVal) * 82))}px" title="浏览 ${d.views}"></div>
            <div class="bar-dl" style="height:${Math.max(4, Math.round((d.downloads / maxVal) * 82))}px" title="下载 ${d.downloads}"></div>
          </div>
          <span class="chart-label">${d.date.slice(5)}</span>
        `;
        chart.append(col);
      }
      wrap.append(chartTitle, chart);
    }

    const logTable = document.createElement("div");
    logTable.className = "table-wrap";
    if (!a.recent_logs.length) {
      logTable.innerHTML = `<p class="muted">暂无访问日志。</p>`;
    } else {
      const tbl = document.createElement("table");
      tbl.innerHTML = `<thead><tr><th>时间</th><th>事件</th><th>来源 IP</th><th>客户端</th></tr></thead><tbody></tbody>`;
      const tb = tbl.querySelector("tbody");
      for (const l of a.recent_logs) {
        const tr = document.createElement("tr");
        const actLabel = l.action === "download" ? "下载" : l.action === "auth_failed" ? "密码错误" : "浏览";
        const cls = l.action === "auth_failed" ? "danger" : l.action === "download" ? "s3" : "ok";
        tr.innerHTML = `
          <td class="mono">${fmtDate(l.created_at)}</td>
          <td><span class="badge ${cls}">${actLabel}</span></td>
          <td class="mono"></td>
          <td class="mono" style="max-width:260px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;"></td>
        `;
        tr.children[2].textContent = l.client_ip || "—";
        tr.children[3].textContent = l.user_agent || "—";
        tr.children[3].title = l.user_agent || "";
        tb.append(tr);
      }
      logTable.append(tbl);
    }
    wrap.append(logTable);

    openModal({
      title: `访问分析 · ${share.node_name || share.token.slice(0, 8)}`,
      bodyEl: wrap,
      wide: true,
      footerButtons: [makeBtn("关闭", "", closeModal)],
    });
  } catch (e) {
    toast("加载分析失败：" + e.message, "err");
  }
}

async function openSessionsModal() {
  try {
    const data = await api("/uploads/");
    const sessions = data.sessions || [];
    const wrap = document.createElement("div");
    if (!sessions.length) {
      wrap.innerHTML = `<p class="muted">当前没有未完成的分片上传会话。</p>`;
    } else {
      const tbl = document.createElement("table");
      tbl.innerHTML = `<thead><tr><th>文件名</th><th>大小</th><th>后端</th><th>过期时间</th><th>操作</th></tr></thead><tbody></tbody>`;
      const tb = tbl.querySelector("tbody");
      for (const s of sessions) {
        const tr = document.createElement("tr");
        tr.innerHTML = `<td></td><td class="mono">${fmtBytes(s.total_size)}</td><td><span class="badge ${s.backend === "s3" ? "s3" : "local"}">${s.backend}</span></td><td class="mono">${fmtDate(s.expires_at)}</td><td></td>`;
        tr.children[0].textContent = s.name;
        tr.children[4].append(
          makeBtn("放弃", "danger secondary sm", async () => {
            await api("/uploads/" + s.id, "DELETE");
            tr.remove();
            toast("已清理未完成会话", "ok");
          }),
        );
        tb.append(tr);
      }
      wrap.append(tbl);
    }
    openModal({ title: "待续传分片会话", bodyEl: wrap, wide: true, footerButtons: [makeBtn("关闭", "", closeModal)] });
  } catch (e) {
    toast(e.message, "err");
  }
}

function openSettingsModal() {
  const wrap = document.createElement("div");
  wrap.innerHTML = `
    <h4 style="margin:0 0 10px;">个人资料与邮箱绑定</h4>
    <div class="field"><label>昵称</label><input id="set-nick" type="text" /></div>
    <div class="field"><label>邮箱</label><input id="set-email" type="email" placeholder="name@example.com" /></div>
    <div class="field"><label>个人简介</label><textarea id="set-bio" rows="2"></textarea></div>
    <div style="margin-bottom:20px;"><button type="button" id="set-save-profile">保存资料</button></div>

    <hr style="border:none;border-top:1px solid var(--border);margin:16px 0;" />
    <h4 style="margin:0 0 10px;">修改密码（会吊销所有旧会话）</h4>
    <div class="field"><label>当前密码</label><input id="set-old-pw" type="password" /></div>
    <div class="field"><label>新密码（8–72 位）</label><input id="set-new-pw" type="password" /></div>
    <div style="margin-bottom:20px;"><button type="button" id="set-save-pw" class="secondary">更新密码</button></div>

    <hr style="border:none;border-top:1px solid var(--border);margin:16px 0;" />
    <h4 style="margin:0 0 10px;color:var(--danger);">危险区域 · 注销账号</h4>
    <div class="field"><label>输入登录密码确认永久删除账号及所有文件</label><input id="set-del-pw" type="password" /></div>
    <button type="button" id="set-del-btn" class="danger secondary">永久注销账号</button>
  `;
  wrap.querySelector("#set-nick").value = state.user.nickname || "";
  wrap.querySelector("#set-email").value = state.user.email || "";
  wrap.querySelector("#set-bio").value = state.user.bio || "";

  wrap.querySelector("#set-save-profile").onclick = async () => {
    try {
      const res = await api("/auth/me", "PATCH", {
        nickname: wrap.querySelector("#set-nick").value.trim(),
        bio: wrap.querySelector("#set-bio").value.trim(),
        email: wrap.querySelector("#set-email").value.trim(),
      });
      state.user = res.user;
      $("user-name").textContent = state.user.nickname || state.user.username;
      $("user-sub").textContent = state.user.email || "@" + state.user.username;
      toast("资料已更新", "ok");
    } catch (e) {
      toast(e.message, "err");
    }
  };

  wrap.querySelector("#set-save-pw").onclick = async () => {
    try {
      await api("/auth/password", "POST", {
        old_password: wrap.querySelector("#set-old-pw").value,
        new_password: wrap.querySelector("#set-new-pw").value,
      });
      saveSession("", "");
      toast("密码已修改，请重新登录", "ok");
      location.reload();
    } catch (e) {
      toast(e.message, "err");
    }
  };

  wrap.querySelector("#set-del-btn").onclick = async () => {
    const pw = wrap.querySelector("#set-del-pw").value;
    if (!pw) return toast("请输入密码确认注销", "err");
    try {
      await api("/auth/me", "DELETE", { password: pw });
      saveSession("", "");
      location.reload();
    } catch (e) {
      toast(e.message, "err");
    }
  };

  openModal({ title: "账号与安全设置", bodyEl: wrap, footerButtons: [makeBtn("关闭", "secondary", closeModal)] });
}

// ---------------- Trash View ----------------
async function loadTrash() {
  try {
    const data = await api("/trash/");
    const items = data.items || [];
    const tbody = $("trash-rows");
    tbody.replaceChildren();
    $("trash-empty").hidden = items.length > 0;

    for (const n of items) {
      const tr = document.createElement("tr");
      tr.innerHTML = `
        <td><div class="file-cell"><span class="file-icon">${fileIcon(n.name, n.is_dir)}</span><span></span></div></td>
        <td class="mono">${fmtBytes(n.size)}</td>
        <td class="mono">${fmtDate(n.deleted_at)}</td>
        <td><div class="ops"></div></td>
      `;
      tr.querySelector(".file-cell span:last-child").textContent = n.name;
      const ops = tr.querySelector(".ops");
      ops.append(
        makeBtn("恢复", "secondary sm", async () => {
          try {
            await api(`/trash/${n.id}/restore`, "POST");
            toast(`已恢复「${n.name}」`, "ok");
            await Promise.all([loadTrash(), refreshStatsAndPolicy()]);
          } catch (e) {
            toast("恢复失败：" + e.message, "err");
          }
        }),
        makeBtn("彻底删除", "danger secondary sm", async () => {
          try {
            await api(`/trash/${n.id}`, "DELETE");
            toast(`已彻底删除「${n.name}」`, "ok");
            await Promise.all([loadTrash(), refreshStatsAndPolicy()]);
          } catch (e) {
            toast(e.message, "err");
          }
        }),
      );
      tbody.append(tr);
    }
  } catch (e) {
    toast("加载回收站失败：" + e.message, "err");
  }
}

$("btn-empty-trash").onclick = () => {
  const p = document.createElement("p");
  p.textContent = "确定清空回收站？清空后文件将永久删除，底层孤儿内容块也会被立即回收。";
  openModal({
    title: "清空回收站",
    bodyEl: p,
    footerButtons: [
      makeBtn("取消", "secondary", closeModal),
      makeBtn("确认清空", "danger", async () => {
        try {
          await api("/trash/", "DELETE");
          closeModal();
          toast("回收站已清空", "ok");
          await Promise.all([loadTrash(), refreshStatsAndPolicy()]);
        } catch (e) {
          toast(e.message, "err");
        }
      }),
    ],
  });
};

// ---------------- Shares View ----------------
async function loadShares() {
  try {
    const data = await api("/shares/");
    const shares = data.shares || [];
    const tbody = $("share-rows");
    tbody.replaceChildren();
    $("shares-empty").hidden = shares.length > 0;

    for (const sh of shares) {
      const tr = document.createElement("tr");
      const link = `${location.origin}/share.html#${sh.token}`;
      const expired = sh.expires_at && new Date(sh.expires_at) <= new Date();

      const tdTarget = document.createElement("td");
      tdTarget.innerHTML = `<div class="file-cell"><span class="file-icon">${sh.node_is_dir ? "📁" : "📄"}</span><span></span></div>`;
      tdTarget.querySelector(".file-cell span:last-child").textContent = sh.node_name || sh.node_id;

      const tdSec = document.createElement("td");
      const badges = [];
      if (sh.has_password) badges.push('<span class="badge lock">🔒 提取码</span>');
      else badges.push('<span class="badge local">公开</span>');
      if (sh.max_downloads) badges.push(`<span class="badge s3">限 ${sh.max_downloads} 次</span>`);
      if (expired) badges.push('<span class="badge danger">已过期</span>');
      tdSec.innerHTML = badges.join(" ");

      const tdCount = document.createElement("td");
      tdCount.className = "mono";
      tdCount.textContent = `浏览 ${sh.visit_count} · 下载 ${sh.download_count}`;

      const tdExp = document.createElement("td");
      tdExp.className = "mono";
      tdExp.textContent = sh.expires_at ? fmtDate(sh.expires_at) : "永久有效";

      const tdOps = document.createElement("td");
      const ops = document.createElement("div");
      ops.className = "ops";
      ops.append(
        makeBtn("复制链接", "secondary sm", () => copyText(link)),
        makeBtn("访问分析", "secondary sm", () => openShareAnalyticsModal(sh)),
        makeBtn("撤销", "danger secondary sm", async () => {
          try {
            await api("/shares/" + sh.id, "DELETE");
            toast("已撤销分享", "ok");
            await Promise.all([loadShares(), refreshStatsAndPolicy()]);
          } catch (e) {
            toast(e.message, "err");
          }
        }),
      );
      tdOps.append(ops);
      tr.append(tdTarget, tdSec, tdCount, tdExp, tdOps);
      tbody.append(tr);
    }
  } catch (e) {
    toast("加载分享列表失败：" + e.message, "err");
  }
}

// ---------------- Storage & Tiering View ----------------
async function loadStorageView() {
  await refreshStatsAndPolicy();
  const s = state.stats;
  const p = state.policy;
  if (!s || !p) return;

  $("storage-kpis").innerHTML = `
    <div class="kpi"><small>有效文件占用</small><strong>${fmtBytes(s.live_bytes)}</strong></div>
    <div class="kpi"><small>本地存储 (Local)</small><strong>${fmtBytes(s.local_bytes)}</strong></div>
    <div class="kpi"><small>对象存储 (S3)</small><strong>${fmtBytes(s.s3_bytes)}</strong></div>
    <div class="kpi"><small>内容去重节省</small><strong style="color:var(--success)">${fmtBytes(s.dedup_saved_bytes)}</strong></div>
  `;
  $("storage-policy-desc").textContent =
    `默认写入后端：${p.default_backend.toUpperCase()} · 已装配后端：${p.available_backends.join(", ").toUpperCase()} · ` +
    (p.s3_configured
      ? p.auto_tier_threshold > 0
        ? `后台已开启热点自动迁移（下载 ≥ ${p.auto_tier_threshold} 次且 ≥ ${fmtBytes(p.auto_tier_min_size)} 自动迁至 S3）`
        : "S3 已就绪；可随时点击下方按钮或配置 NETDISK_AUTO_TIER_THRESHOLD 自动迁移高频下载文件到 S3。"
      : "当前尚未配置 S3 对象存储（可在 .env 中填写 NETDISK_S3_* 启用混合存储与热点分层）。");
  $("btn-run-tier").disabled = !p.s3_configured;
}

$("btn-run-tier").onclick = async () => {
  const threshold = Number($("tier-threshold").value) || 3;
  const min_size = Number($("tier-minsize").value) || 0;
  $("btn-run-tier").disabled = true;
  try {
    const res = await api("/storage/tier/run", "POST", { threshold, min_size });
    toast(`热点扫描完成：已将 ${res.migrated} 个高频文件迁移至 S3`, "ok");
    await loadStorageView();
  } catch (e) {
    toast(e.message, "err");
  } finally {
    $("btn-run-tier").disabled = !state.policy?.s3_configured;
  }
};

// ---------------- Event Bindings ----------------
$("btn-logout").onclick = async () => {
  try {
    await api("/auth/logout", "POST");
  } catch {}
  saveSession("", "");
  location.reload();
};
$("btn-settings").onclick = openSettingsModal;
$("btn-mkdir").onclick = openMkdirModal;
$("btn-sessions").onclick = openSessionsModal;
$("btn-refresh").onclick = () => {
  loadFiles();
  refreshStatsAndPolicy();
};
$("btn-refresh-trash").onclick = loadTrash;
$("btn-refresh-shares").onclick = loadShares;
$("btn-refresh-storage").onclick = loadStorageView;

$("btn-search").onclick = () => {
  state.searchQuery = $("search-input").value.trim();
  $("btn-clear-search").hidden = !state.searchQuery;
  state.offset = 0;
  loadFiles();
};
$("search-input").addEventListener("keydown", (e) => {
  if (e.key === "Enter") $("btn-search").click();
});
$("btn-clear-search").onclick = () => {
  $("search-input").value = "";
  state.searchQuery = "";
  $("btn-clear-search").hidden = true;
  state.offset = 0;
  loadFiles();
};
$("filter-type").onchange = (e) => {
  state.filterType = e.target.value;
  state.offset = 0;
  loadFiles();
};
$("sort-by").onchange = (e) => {
  const [sort, order] = e.target.value.split(":");
  state.sort = sort;
  state.order = order;
  state.offset = 0;
  loadFiles();
};
$("btn-prev-page").onclick = () => {
  state.offset = Math.max(0, state.offset - state.limit);
  loadFiles();
};
$("btn-next-page").onclick = () => {
  state.offset += state.limit;
  loadFiles();
};

// Drag & drop + file picker
$("btn-choose-files").onclick = () => $("file-picker").click();
$("file-picker").onchange = (e) => {
  handleUploadFiles(e.target.files);
  e.target.value = "";
};
const dz = $("dropzone");
["dragenter", "dragover"].forEach((ev) =>
  dz.addEventListener(ev, (e) => {
    e.preventDefault();
    dz.classList.add("dragover");
  }),
);
["dragleave", "drop"].forEach((ev) =>
  dz.addEventListener(ev, (e) => {
    e.preventDefault();
    dz.classList.remove("dragover");
  }),
);
dz.addEventListener("drop", (e) => handleUploadFiles(e.dataTransfer.files));

// Boot
if (state.token || state.refreshToken) {
  enterApp().catch(() => {
    saveSession("", "");
    $("auth-view").hidden = false;
    $("app-view").hidden = true;
  });
}
