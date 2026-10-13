// Confirmation modals (native <dialog>) and copy-to-clipboard buttons.

const confirmDialog = document.getElementById("confirm-dialog");
const slot = (root, name) => root.querySelectorAll(`[data-slot="${name}"]`);

// Forms with data-confirm ask first. data-confirm-title / -ok set the heading
// and button; data-confirm-safe makes the button blue instead of red.
document.addEventListener("submit", (e) => {
  const form = e.target;
  if (!form.dataset.confirm || form.dataset.confirmed) return;
  e.preventDefault();

  slot(confirmDialog, "title")[0].textContent = form.dataset.confirmTitle || "¿Seguro?";
  slot(confirmDialog, "message")[0].textContent = form.dataset.confirm;
  const ok = slot(confirmDialog, "ok")[0];
  ok.textContent = form.dataset.confirmOk || "Confirmar";
  ok.classList.toggle("danger", !("confirmSafe" in form.dataset));

  confirmDialog.addEventListener(
    "close",
    () => {
      if (confirmDialog.returnValue !== "ok") return;
      form.dataset.confirmed = "1";
      form.requestSubmit();
    },
    { once: true }
  );
  confirmDialog.returnValue = "";
  confirmDialog.showModal();
});

// Forms with data-match keep their submit button disabled until the
// "confirm" field matches it (typing a database's name to delete it).
document.querySelectorAll("form[data-match]").forEach((form) => {
  const input = form.elements.confirm;
  const btn = form.querySelector("button");
  const sync = () => (btn.disabled = input.value !== form.dataset.match);
  input.addEventListener("input", sync);
  sync();
});

// Close buttons, and clicks on the backdrop, close any modal.
document.addEventListener("click", (e) => {
  const dialog = e.target.closest("dialog");
  if (!dialog) return;
  if (e.target.closest("[data-close]") || e.target === dialog) dialog.close();
});

document.addEventListener("click", async (e) => {
  const btn = e.target.closest("[data-copy]");
  if (!btn) return;
  try {
    await navigator.clipboard.writeText(btn.dataset.copy);
    const label = btn.textContent;
    btn.textContent = "Copiado";
    setTimeout(() => (btn.textContent = label), 1500);
  } catch {
    btn.textContent = "No se pudo copiar";
  }
});

// Buttons with data-busy show it and disable themselves while the form posts,
// so a slow CREATE DATABASE isn't submitted twice.
document.addEventListener("submit", (e) => {
  if (e.defaultPrevented) return;
  const btn = e.target.querySelector("button[data-busy]");
  if (!btn) return;
  btn.textContent = btn.dataset.busy;
  setTimeout(() => (btn.disabled = true));
});

// While a backup runs, its page reloads itself to show the log.
const auto = document.querySelector("[data-autorefresh]");
if (auto) {
  setTimeout(() => window.location.replace(window.location.pathname), Number(auto.dataset.autorefresh) * 1000);
}

// Usage bars: the width comes from data-w (the CSP blocks inline styles).
document.querySelectorAll("[data-w]").forEach((el) => {
  el.style.width = `${Math.min(100, Number(el.dataset.w))}%`;
});

// "Actualizado hace …": the server stamps when it read the data (data-since,
// Unix ms); the exact time stays in the tooltip.
const since = document.querySelectorAll("[data-since]");
const ago = (ms) => {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 10) return "hace un momento";
  if (s < 60) return `hace ${s} s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `hace ${m} min`;
  const h = Math.floor(m / 60);
  return h < 24 ? `hace ${h} h` : `hace ${Math.floor(h / 24)} d`;
};
const tick = () => since.forEach((el) => {
  el.textContent = `Actualizado ${ago(Date.now() - Number(el.dataset.since))}`;
});
if (since.length) {
  tick();
  setInterval(tick, 5000);
}

// Tooltips: [data-tip] shows its text below the element on hover or focus
// (a tap focuses it on touch screens).
let tooltip;
const hideTip = () => tooltip?.remove();
const showTip = (el) => {
  hideTip();
  tooltip = document.createElement("div");
  tooltip.className = "tooltip";
  tooltip.textContent = el.dataset.tip;
  document.body.append(tooltip);
  const r = el.getBoundingClientRect();
  const t = tooltip.getBoundingClientRect();
  const left = Math.min(Math.max(16, r.left), window.innerWidth - t.width - 16);
  const below = r.bottom + 6 + t.height < window.innerHeight;
  tooltip.style.left = `${left}px`;
  tooltip.style.top = `${below ? r.bottom + 6 : r.top - t.height - 6}px`;
};
document.querySelectorAll("[data-tip]").forEach((el) => {
  el.addEventListener("mouseenter", () => showTip(el));
  el.addEventListener("mouseleave", hideTip);
  el.addEventListener("focus", () => showTip(el));
  el.addEventListener("blur", hideTip);
});
window.addEventListener("scroll", hideTip, true);

// Buttons with data-open="<dialog id>" open that modal.
document.addEventListener("click", (e) => {
  const btn = e.target.closest("[data-open]");
  if (btn) document.getElementById(btn.dataset.open)?.showModal();
});

// Backup picker on the new database form: a click picks, a double click
// picks and closes; Seleccionar copies it into the form's hidden field.
const picker = document.getElementById("backup-dialog");
if (picker) {
  const form = document.querySelector('form[action="/databases"]');
  const key = slot(form, "backup-key")[0];
  const label = slot(form, "backup-label")[0];
  const clear = slot(form, "backup-clear")[0];
  const ok = slot(picker, "backup-ok")[0];
  const filter = slot(picker, "backup-filter")[0];

  const setLabel = (text) => {
    const [folder, rest] = text.split("|");
    const strong = document.createElement("strong");
    strong.textContent = folder;
    label.replaceChildren(strong, ` · ${rest}`);
  };

  picker.addEventListener("change", () => (ok.disabled = false));
  picker.addEventListener("dblclick", (e) => {
    if (e.target.closest(".pick")) picker.close("ok");
  });
  filter.addEventListener("input", () => {
    const q = filter.value.trim().toLowerCase();
    picker.querySelectorAll(".pick-group").forEach((g) => (g.hidden = !g.dataset.folder.includes(q)));
  });
  picker.addEventListener("close", () => {
    if (picker.returnValue !== "ok") return;
    const radio = picker.querySelector("input[name=pick]:checked");
    if (!radio) return;
    key.value = radio.value;
    setLabel(radio.nextElementSibling.dataset.label);
    clear.hidden = false;
  });
  // returnValue sticks between openings: reset it each time.
  picker.addEventListener("cancel", () => (picker.returnValue = ""));
  form.querySelector('[data-open="backup-dialog"]').addEventListener("click", () => (picker.returnValue = ""));
  clear.addEventListener("click", () => {
    key.value = "";
    label.textContent = "Vacía";
    clear.hidden = true;
    picker.querySelectorAll("input[name=pick]").forEach((r) => (r.checked = false));
    ok.disabled = true;
  });
}
