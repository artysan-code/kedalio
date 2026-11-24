"use strict";
// The pages' one script: the WebAuthn calls and the JSON API of the sign-in, of the approval of a new client and of the first passkey's setup.
(() => {
  const S = JSON.parse(document.getElementById("strings").textContent);
  const D = document.body.dataset;
  const $ = (id) => document.getElementById(id);
  const fail = (msg) => {
    $("error").textContent = msg;
  };
  const b64u = (buf) =>
    btoa(String.fromCharCode(...new Uint8Array(buf))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  const unb64u = (s) =>
    Uint8Array.from(
      atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4)),
      (c) => c.charCodeAt(0),
    );
  const post = async (path, body) => {
    const r = await fetch(path, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body ?? {}),
    });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error ?? r.statusText);
    return j;
  };
  const done = (next) => {
    if (next) return location.assign(next);
    $("main").classList.add("hidden");
    $("done").classList.remove("hidden");
  };
  const guard = (fn) => async (e) => {
    e?.preventDefault();
    fail("");
    try {
      await fn(e);
    } catch (err) {
      fail(S.failed.replace("{why}", err.message));
    }
  };
  if (D.view === "sign-in") {
    $("passkey").onclick = guard(async () => {
      const o = await post("/api/auth/passkey/options");
      const cred = await navigator.credentials.get({
        publicKey: { ...o, challenge: unb64u(o.challenge), allowCredentials: [] },
      });
      await post("/api/auth/passkey", {
        assertion: {
          credential: b64u(cred.rawId),
          authenticatorData: b64u(cred.response.authenticatorData),
          clientDataJSON: b64u(cred.response.clientDataJSON),
          signature: b64u(cred.response.signature),
        },
      });
      done(D.next);
    });
  }
  const show = (id, on) => $(id).classList.toggle("hidden", !on);
  const clock = (s) => Math.floor(s / 60) + ":" + String(s % 60).padStart(2, "0");
  // approval waits for the owner to approve a code: the page asks for the code when begin is called, not before (a link
  // scanner that opens the page must not make one). handle gets each answer that is neither pending nor expired and
  // says whether it dealt with it; anything else is a refusal.
  const approval = (startBody, handle) => {
    let waiter = "";
    let ticking = 0;
    let waiting = 0;
    const stop = () => {
      clearInterval(ticking);
      clearTimeout(waiting);
    };
    const ended = (text) => {
      stop();
      waiter = "";
      $("code").textContent = text;
      $("left").textContent = "";
      show("how", false);
      show("cancel", false);
      show("again", true);
    };
    const poll = async () => {
      let r;
      try {
        r = await post("/api/auth/approval/poll", { waiter });
      } catch {
        // a failed poll (the network, a limit) is tried again, not shown
        waiting = setTimeout(poll, 2000);
        return;
      }
      if (r.state === "pending") {
        waiting = setTimeout(poll, 2000);
        return;
      }
      if (r.state === "expired") return ended(S.approveExpired);
      stop();
      try {
        if (!(await handle(r, waiter))) ended(S.approveDenied);
      } catch (err) {
        ended("");
        fail(S.failed.replace("{why}", err.message));
      }
    };
    const begin = guard(async () => {
      stop();
      show("again", false);
      $("code").textContent = S.approveWait;
      let r;
      try {
        r = await post("/api/auth/approval/start", startBody());
      } catch (err) {
        ended("");
        throw err;
      }
      waiter = r.waiter;
      const deadline = Date.now() + r.expires_in * 1000;
      const tick = () =>
        $("left").textContent = S.approveLeft.replace(
          "{time}",
          clock(Math.max(0, Math.ceil((deadline - Date.now()) / 1000))),
        );
      tick();
      ticking = setInterval(tick, 1000);
      $("code").textContent = r.code;
      $("cli").textContent = S.approveCli.replace("{code}", r.code);
      $("ask").textContent = S.approveAsk.replace("{code}", r.code);
      show("ask", r.local);
      show("how", true);
      show("cancel", true);
      waiting = setTimeout(poll, 2000);
    });
    $("again").onclick = begin;
    $("cancel").onclick = guard(async () => {
      await post("/api/auth/approval/cancel", { waiter });
      clearTimeout(waiting);
      await poll();
    });
    return begin;
  };
  if (D.view === "approval") {
    const params = Object.fromEntries(new FormData($("params")));
    approval(() => ({ params }), (r) => {
      if (!r.redirect) return false;
      location.assign(r.redirect);
      return true;
    })();
  }
  if (D.view === "setup") {
    const begin = approval(() => ({ purpose: "passkey" }), async (r, waiter) => {
      if (r.state !== "approved" || !r.options) return false;
      const o = r.options;
      const cred = await navigator.credentials.create({
        publicKey: {
          ...o,
          challenge: unb64u(o.challenge),
          user: { ...o.user, id: unb64u(o.user.id) },
          excludeCredentials: o.excludeCredentials.map((c) => ({ ...c, id: unb64u(c.id) })),
        },
      });
      await post("/api/auth/approval/passkey", {
        waiter,
        label: navigator.platform || "passkey",
        registration: {
          id: b64u(cred.rawId),
          response: {
            attestationObject: b64u(cred.response.attestationObject),
            clientDataJSON: b64u(cred.response.clientDataJSON),
          },
        },
      });
      done("");
      return true;
    });
    $("create").onclick = () => {
      show("create", false);
      show("wait", true);
      begin();
    };
  }
})();
