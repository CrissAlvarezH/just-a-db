// Confirm before destructive forms, and copy-to-clipboard buttons.
document.addEventListener("submit", (e) => {
  const msg = e.target.dataset.confirm;
  if (msg && !window.confirm(msg)) e.preventDefault();
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
