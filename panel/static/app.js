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
