const $ = (id) => document.getElementById(id);
const token = encodeURIComponent(location.hash.slice(1));
let trail = [];

function fmtBytes(n) {
  if (!n || n <= 0) return "0 B";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0,
    v = n;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return (i === 0 ? v : v.toFixed(1)) + " " + u[i];
}

function previewKind(name) {
  const ext = (name.split(".").pop() || "").toLowerCase();
  if (["png", "jpg", "jpeg", "gif", "webp"].includes(ext)) return "image";
  if (["mp4", "webm"].includes(ext)) return "video";
  if (["mp3", "wav", "ogg"].includes(ext)) return "audio";
  if (ext === "pdf") return "pdf";
  if (["txt", "md", "json", "csv", "log", "go", "js", "css", "py"].includes(ext)) return "text";
  return null;
}

function openPreview(node, kind) {
  const url = `/api/v1/share/${token}/download?node_id=${node.id}&inline=1`;
  const root = $("modal-root");
  root.replaceChildren();
  const modal = document.createElement("div");
  modal.className = "modal wide";
  modal.innerHTML = `<div class="modal-header"><h3></h3><button type="button" class="ghost sm">✕</button></div><div class="modal-body"><div class="preview-box"></div></div>`;
  modal.querySelector("h3").textContent = "预览 · " + node.name;
  modal.querySelector("button").onclick = () => (root.hidden = true);
  const box = modal.querySelector(".preview-box");

  if (kind === "image") {
    const img = document.createElement("img");
    img.src = url;
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
  } else {
    const pre = document.createElement("pre");
    pre.textContent = "加载中…";
    box.append(pre);
    fetch(url)
      .then((r) => r.text())
      .then((t) => (pre.textContent = t.slice(0, 150000)))
      .catch((e) => (pre.textContent = "加载失败：" + e.message));
  }
  root.append(modal);
  root.hidden = false;
  root.onclick = (e) => {
    if (e.target === root) root.hidden = true;
  };
}

function renderCrumbs() {
  const c = $("share-crumbs");
  c.replaceChildren();
  const rootBtn = document.createElement("button");
  rootBtn.type = "button";
  rootBtn.textContent = "分享根目录";
  rootBtn.onclick = () => {
    trail = [];
    load();
  };
  c.append(rootBtn);
  trail.forEach((item, idx) => {
    const sep = document.createElement("span");
    sep.className = "sep";
    sep.textContent = "/";
    const b = document.createElement("button");
    b.type = "button";
    b.textContent = item.name;
    b.onclick = () => {
      trail = trail.slice(0, idx + 1);
      load(item.id);
    };
    c.append(sep, b);
  });
}

async function load(nodeID) {
  $("error-card").hidden = true;
  try {
    const r = await fetch(`/api/v1/share/${token}${nodeID ? "?node_id=" + encodeURIComponent(nodeID) : ""}`);
    const data = await r.json();
    if (!r.ok) {
      if (r.status === 403 && (data.error?.code === "share_password_required" || data.error?.code === "share_password_invalid")) {
        $("share-title").textContent = "需要提取码";
        $("unlock-card").hidden = false;
        $("content-card").hidden = true;
        return;
      }
      throw new Error(data.error?.message || "无法打开分享");
    }

    $("unlock-card").hidden = true;
    $("content-card").hidden = false;
    $("share-title").textContent = data.node.name;

    const metaParts = [];
    if (data.expires_at) metaParts.push("有效至 " + new Date(data.expires_at).toLocaleString("zh-CN", { hour12: false }));
    else metaParts.push("永久有效");
    if (data.max_downloads) metaParts.push(`已下载 ${data.download_count} / ${data.max_downloads} 次`);
    $("share-meta").textContent = metaParts.join(" · ");

    renderCrumbs();
    const tbody = $("share-rows");
    tbody.replaceChildren();

    const items = data.node.is_dir ? data.children : [data.node];
    for (const n of items) {
      const tr = document.createElement("tr");
      const tdName = document.createElement("td");
      tdName.textContent = (n.is_dir ? "📁 " : "📄 ") + n.name;
      const tdSize = document.createElement("td");
      tdSize.className = "mono";
      tdSize.textContent = n.is_dir ? "—" : fmtBytes(n.size);

      const tdOps = document.createElement("td");
      const ops = document.createElement("div");
      ops.className = "ops";
      if (n.is_dir) {
        const b = document.createElement("button");
        b.type = "button";
        b.className = "secondary sm";
        b.textContent = "进入目录";
        b.onclick = () => {
          trail.push({ id: n.id, name: n.name });
          load(n.id);
        };
        ops.append(b);
      } else {
        const pk = previewKind(n.name);
        if (pk) {
          const pb = document.createElement("button");
          pb.type = "button";
          pb.className = "secondary sm";
          pb.textContent = "预览";
          pb.onclick = () => openPreview(n, pk);
          ops.append(pb);
        }
        const dl = document.createElement("button");
        dl.type = "button";
        dl.className = "sm";
        dl.textContent = "下载";
        dl.onclick = () => {
          location.href = `/api/v1/share/${token}/download?node_id=${n.id}`;
        };
        ops.append(dl);
      }
      tdOps.append(ops);
      tr.append(tdName, tdSize, tdOps);
      tbody.append(tr);
    }
  } catch (e) {
    $("unlock-card").hidden = true;
    $("content-card").hidden = true;
    $("error-card").hidden = false;
    $("share-title").textContent = "无法访问分享";
    $("error-msg").textContent = e.message;
  }
}

$("unlock-form").onsubmit = async (e) => {
  e.preventDefault();
  $("unlock-error").textContent = "";
  try {
    const r = await fetch(`/api/v1/share/${token}/verify`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password: $("share-pw").value }),
    });
    const data = await r.json();
    if (!r.ok) throw new Error(data.error?.message || "提取码错误");
    await load();
  } catch (err) {
    $("unlock-error").textContent = err.message;
  }
};

if (!token) {
  $("share-title").textContent = "分享链接不完整";
  $("error-card").hidden = false;
  $("error-msg").textContent = "URL 缺少分享 token。";
} else {
  load();
}
