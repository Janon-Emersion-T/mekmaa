(() => {
  const whatsapp = document.getElementById("students-share-whatsapp");
  const email = document.getElementById("students-share-email");
  if (!whatsapp || !email) return;

  // Keep report filters, but exclude unrelated page actions and pagination.
  const current = new URL(window.location.href);
  const report = new URL("/admin/students", current.origin);
  for (const key of ["division", "search", "status", "direction"]) {
    if (current.searchParams.has(key)) {
      report.searchParams.set(key, current.searchParams.get(key));
    }
  }
  report.searchParams.set("format", "pdf");
  const subject = "Mekmaa — Students list";
  const message = `${subject}\n\n${report.href}\n\nSign in with an authorized Mekmaa account to view this report.`;
  whatsapp.href = "https://wa.me/?text=" + encodeURIComponent(message);
  email.href = "mailto:?subject=" + encodeURIComponent(subject) + "&body=" + encodeURIComponent(message);
  whatsapp.hidden = false;
  email.hidden = false;
  document.getElementById("students-share-note").hidden = false;
})();
