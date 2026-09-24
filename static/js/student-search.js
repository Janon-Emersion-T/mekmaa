(() => {
  const form = document.getElementById("student-search-form");
  if (!form || !window.fetch || !window.AbortController) return;

  const search = form.elements.search;
  const results = document.getElementById("student-search-results");
  const status = document.getElementById("student-search-status");
  const initialDivision = form.elements.division.value;
  let timer;
  let controller;
  let revision = 0;

  function cancelPending() {
    clearTimeout(timer);
    if (controller) controller.abort();
    revision += 1;
    results.removeAttribute("aria-busy");
  }

  async function updateResults() {
    cancelPending();
    // Division changes also update the surrounding workspace navigation.
    if (form.elements.division.value !== initialDivision) {
      form.submit();
      return;
    }
    const requestRevision = revision;
    controller = new AbortController();
    const url = new URL(form.getAttribute("action"), window.location.href);
    url.search = new URLSearchParams(new FormData(form)).toString();
    url.searchParams.set("page", "1");
    results.setAttribute("aria-busy", "true");
    status.textContent = "Searching…";

    try {
      const response = await fetch(url, {
        signal: controller.signal,
        credentials: "same-origin",
        headers: { Accept: "text/html" },
      });
      if (!response.ok || response.redirected) throw new Error("Search unavailable");
      const html = await response.text();
      if (requestRevision !== revision) return;
      const page = new DOMParser().parseFromString(html, "text/html");
      const ids = ["student-search-results", "student-result-summary", "student-total", "student-status-totals", "student-registered-total", "student-export-form"];
      const replacements = ids.map((id) => ({
        current: document.getElementById(id), next: page.getElementById(id),
      }));
      if (replacements.some(({ current, next }) => !current || !next)) {
        throw new Error("Missing student results");
      }
      replacements.forEach(({ current, next }) => { current.innerHTML = next.innerHTML; });
      url.hash = window.location.hash;
      window.history.replaceState(null, "", url);
      status.textContent = page.getElementById("student-result-summary").textContent.trim().replace(/\s+/g, " ");
    } catch (error) {
      if (requestRevision !== revision || error.name === "AbortError") return;
      status.textContent = "Could not update students. Please try again or reload the page. The previous results are still shown.";
    } finally {
      if (requestRevision === revision) results.removeAttribute("aria-busy");
    }
  }

  search.addEventListener("input", (event) => {
    cancelPending();
    status.textContent = "";
    if (!event.isComposing) timer = setTimeout(updateResults, 300);
  });
  search.addEventListener("compositionstart", cancelPending);
  search.addEventListener("compositionend", () => {
    cancelPending();
    timer = setTimeout(updateResults, 300);
  });
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    updateResults();
  });
  form.querySelectorAll('select, input[type="radio"][name="status"]').forEach((control) => {
    control.addEventListener("change", updateResults);
  });
})();
