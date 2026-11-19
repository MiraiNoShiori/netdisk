const $ = (id) => document.getElementById(id),
  status = (msg) => ($("status").textContent = msg);

let pc,
  channel,
  room,
  writer,
  cancelled = false,
  pollTimer,
  watchdog,
  received = 0,
  buffers = [],
  queue = Promise.resolve(),
  finished = false,
  ackWait = null,
  transferStart = 0;

const token = sessionStorage.getItem("netdisk-token"),
  invite = location.hash.slice(1),
  limit = 64 * 1024 * 1024;

function fmtBytes(n) {
  if (!n || n <= 0) return "0 B";
  const u = ["B", "KB", "MB", "GB"];
  let i = 0,
    v = n;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return (i === 0 ? v : v.toFixed(1)) + " " + u[i];
}

async function computeSHA256(blob) {
  if (!window.crypto?.subtle || blob.size > limit) return null;
  const buf = await blob.arrayBuffer();
  const sum = await crypto.subtle.digest("SHA-256", buf);
  return Array.from(new Uint8Array(sum))
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}

async function api(path, method = "GET", body) {
  const res = await fetch("/api/v1" + path, {
    method,
    headers: {
      "Content-Type": "application/json",
      ...(token ? { Authorization: "Bearer " + token } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    cache: "no-store",
  });
  if (!res.ok) {
    const e = await res.json();
    throw Error(e.error?.message || res.status);
  }
  return res.status === 204 ? null : res.json();
}

function fail(e) {
  status("传输失败：" + e.message);
  cleanup(false);
}

function cleanup(done) {
  cancelled = true;
  clearTimeout(pollTimer);
  clearTimeout(watchdog);
  channel?.close();
  pc?.close();
  if (writer && !done) {
    writer.abort().catch(() => {});
    writer = null;
  }
  if (room && !invite) api("/p2p/shares/" + room.id, "DELETE").catch(() => {});
}

function peer() {
  if (!window.RTCPeerConnection) throw Error("浏览器不支持 WebRTC");
  const p = new RTCPeerConnection({ iceServers: [] });
  p.onconnectionstatechange = () => {
    if (["failed", "disconnected"].includes(p.connectionState) && !finished && !cancelled) {
      fail(Error("直连中断；请检查两端局域网连通性后重新创建分享"));
    }
  };
  return p;
}

function gather(p) {
  return new Promise((resolve, reject) => {
    if (p.iceGatheringState === "complete") return resolve();
    const t = setTimeout(() => {
      p.removeEventListener("icegatheringstatechange", check);
      reject(Error("收集局域网连接信息超时"));
    }, 15000);
    function check() {
      if (p.iceGatheringState === "complete") {
        clearTimeout(t);
        p.removeEventListener("icegatheringstatechange", check);
        resolve();
      }
    }
    p.addEventListener("icegatheringstatechange", check);
  });
}

function progress(n, total) {
  $("progress").value = total ? n / total : 1;
  const elapsed = Math.max(0.05, (performance.now() - transferStart) / 1000);
  const speed = n / elapsed;
  const eta = speed > 0 && total > n ? Math.ceil((total - n) / speed) : 0;
  const pct = total ? Math.round((n / total) * 100) : 100;
  status(`${pct}% · ${fmtBytes(n)} / ${fmtBytes(total)} · ${fmtBytes(speed)}/s${eta ? ` · 剩余约 ${eta}s` : ""}`);
}

function drain(ch) {
  return new Promise((resolve, reject) => {
    if (ch.readyState !== "open") return reject(Error("连接已关闭"));
    if (ch.bufferedAmount < 1024 * 1024) return resolve();
    const done = () => {
      clearTimeout(t);
      ch.removeEventListener("bufferedamountlow", low);
      ch.removeEventListener("close", closed);
    };
    const low = () => {
        done();
        resolve();
      },
      closed = () => {
        done();
        reject(Error("连接已关闭"));
      },
      t = setTimeout(() => {
        done();
        reject(Error("发送等待超时"));
      }, 30000);
    ch.addEventListener("bufferedamountlow", low, { once: true });
    ch.addEventListener("close", closed, { once: true });
  });
}

$("copy-invite").onclick = async () => {
  const val = $("invite").value;
  if (!val) return;
  try {
    await navigator.clipboard.writeText(val);
    status("邀请链接已复制到剪贴板，发送给局域网内的接收方即可。");
  } catch {
    $("invite").select();
    document.execCommand("copy");
  }
};

$("start").onclick = async () => {
  try {
    const file = $("file").files[0];
    if (!file) throw Error("请先选择文件");
    if (!token) throw Error("请先在网盘页面登录");
    $("start").disabled = true;
    cancelled = false;
    finished = false;
    const expectedHash = await computeSHA256(file);
    if (expectedHash) {
      $("checksum").hidden = false;
      $("checksum").textContent = "发送文件 SHA-256：" + expectedHash;
    }

    room = (await api("/p2p/shares/", "POST", { name: file.name, size: file.size })).share;
    pc = peer();
    channel = pc.createDataChannel("file", { ordered: true });
    channel.bufferedAmountLowThreshold = 256 * 1024;
    channel.onmessage = (e) => {
      if (e.data === "ack" && ackWait) {
        ackWait();
        ackWait = null;
        return;
      }
      if (e.data === "received") {
        finished = true;
        status("✓ 传输完成，对方已成功校验并保存文件");
        cleanup(true);
      }
    };
    channel.onopen = async () => {
      try {
        transferStart = performance.now();
        let sent = 0;
        for (let offset = 0; offset < file.size; offset += 32768) {
          if (cancelled) throw Error("已取消");
          await drain(channel);
          const data = await file.slice(offset, offset + 32768).arrayBuffer();
          const saved = new Promise((resolve, reject) => {
            const timer = setTimeout(() => {
              ackWait = null;
              reject(Error("接收方写盘超时"));
            }, 30000);
            ackWait = () => {
              clearTimeout(timer);
              resolve();
            };
          });
          channel.send(data);
          await saved;
          sent += data.byteLength;
          progress(sent, file.size);
        }
        channel.send(JSON.stringify({ type: "done", sha256: expectedHash || "" }));
        status("文件已发送完毕，等待对方校验并确认保存…");
        watchdog = setTimeout(() => fail(Error("接收方确认超时")), 60000);
      } catch (e) {
        fail(e);
      }
    };

    await pc.setLocalDescription(await pc.createOffer());
    await gather(pc);
    await api("/p2p/shares/" + room.id + "/offer", "POST", { sdp: pc.localDescription.sdp });
    $("invite").value = location.origin + "/p2p.html#" + room.token;
    $("copy-invite").disabled = false;
    status("等待接收方打开邀请链接…");

    async function poll() {
      if (cancelled) return;
      try {
        const current = await api("/p2p/shares/" + room.id);
        if (current.answer) {
          await pc.setRemoteDescription({ type: "answer", sdp: current.answer });
          watchdog = setTimeout(() => {
            if (pc.connectionState !== "connected") fail(Error("局域网直连超时"));
          }, 30000);
          return;
        }
        pollTimer = setTimeout(poll, 1000);
      } catch (e) {
        fail(e);
      }
    }
    poll();
  } catch (e) {
    $("start").disabled = false;
    fail(e);
  }
};

if (invite) {
  $("send").hidden = true;
  $("receive").hidden = false;
  try {
    room = await api("/p2p/" + encodeURIComponent(invite));
    $("filename").textContent = room.name;
    $("details").textContent = `${fmtBytes(room.size)}（${room.size.toLocaleString()} 字节） · 链接有效至 ${new Date(room.expires_at).toLocaleTimeString()}`;
    status("等待你选择保存位置并连接");
  } catch (e) {
    $("accept").disabled = true;
    fail(e);
  }
}

$("accept").onclick = async () => {
  try {
    if (!room?.offer) throw Error("发送方尚未准备好，请稍后刷新");
    $("accept").disabled = true;
    cancelled = false;
    if (window.showSaveFilePicker) {
      const handle = await showSaveFilePicker({ suggestedName: room.name });
      writer = await handle.createWritable();
    } else if (room.size > limit) {
      throw Error("兼容模式上限 64 MiB；请使用 HTTPS 或 localhost 下的 Chrome / Edge");
    }

    pc = peer();
    pc.ondatachannel = (e) => {
      channel = e.channel;
      channel.binaryType = "arraybuffer";
      channel.onclose = () => {
        if (!finished && !cancelled) fail(Error("发送方断开连接"));
      };
      channel.onmessage = (ev) => {
        queue = queue
          .then(async () => {
            if (cancelled) return;
            if (!transferStart) transferStart = performance.now();
            if (typeof ev.data === "string") {
              let doneMsg = { type: ev.data, sha256: "" };
              if (ev.data.startsWith("{")) {
                try {
                  doneMsg = JSON.parse(ev.data);
                } catch {}
              }
              if (doneMsg.type !== "done" || received !== room.size) {
                throw Error("接收长度校验失败");
              }
              if (writer) {
                await writer.close();
                writer = null;
              } else {
                const blob = new Blob(buffers);
                const gotHash = await computeSHA256(blob);
                if (doneMsg.sha256 && gotHash && gotHash !== doneMsg.sha256) {
                  throw Error("SHA-256 完整性校验不一致");
                }
                if (gotHash) {
                  $("checksum").hidden = false;
                  $("checksum").textContent =
                    "✓ SHA-256 端到端校验一致：" + gotHash;
                }
                const url = URL.createObjectURL(blob);
                const a = document.createElement("a");
                a.href = url;
                a.download = room.name;
                a.click();
                setTimeout(() => URL.revokeObjectURL(url), 60000);
                buffers = [];
              }
              finished = true;
              channel.send("received");
              status("✓ 接收完成，文件已保存");
              setTimeout(() => cleanup(true), 500);
              return;
            }
            if (finished) throw Error("结束后收到多余数据");
            received += ev.data.byteLength;
            if (received > room.size) throw Error("收到的数据超过声明大小");
            if (writer) await writer.write(ev.data);
            else buffers.push(ev.data);
            channel.send("ack");
            progress(received, room.size);
          })
          .catch(fail);
      };
    };

    await pc.setRemoteDescription({ type: "offer", sdp: room.offer });
    await pc.setLocalDescription(await pc.createAnswer());
    await gather(pc);
    await api("/p2p/" + encodeURIComponent(invite) + "/answer", "POST", { sdp: pc.localDescription.sdp });
    status("正在建立设备间 P2P 连接…");
    watchdog = setTimeout(() => {
      if (pc.connectionState !== "connected") fail(Error("局域网直连超时"));
    }, 30000);
  } catch (e) {
    $("accept").disabled = false;
    fail(e);
  }
};

$("cancel").onclick = () => {
  cleanup(false);
  status("已取消；刷新页面可重新开始");
};
window.addEventListener("beforeunload", () => {
  pc?.close();
});
