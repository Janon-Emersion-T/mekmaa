(() => {
  "use strict";
  const form = document.getElementById("staff-salary-form");
  if (!form) return;
  const staff = document.getElementById("salary-staff");
  const period = document.getElementById("salary-period");
  const start = document.getElementById("salary-period-start");
  const end = document.getElementById("salary-period-end");
  const generate = document.getElementById("salary-generate-button");
  const calendar = document.getElementById("salary-calendar");
  const days = document.getElementById("salary-calendar-days");
  const selection = document.getElementById("salary-selection");
  const runs = [...document.querySelectorAll("#salary-existing-periods span")].map(node => node.dataset);
  const salaries = [...document.querySelectorAll("#salary-staff-periods span")].map(node => node.dataset);
  let month = new Date();
  let choosingEnd = false;
  const iso = date => date.getFullYear() + "-" + String(date.getMonth() + 1).padStart(2, "0") + "-" + String(date.getDate()).padStart(2, "0");
  const overlaps = (a, b, row) => row.start <= b && row.end >= a;
  const existingSalary = (a, b) => salaries.find(row => row.user === staff.value && row.status !== "void" && overlaps(a, b, row));
  const lockedRun = (a, b) => runs.find(row => ["approved", "closed"].includes(row.status) && overlaps(a, b, row));
  const unavailable = (a, b) => existingSalary(a, b) || lockedRun(a, b);
  const updateSelection = () => {
    const ready = staff.value && start.value && end.value && !unavailable(start.value, end.value);
    generate.disabled = !ready;
    selection.textContent = ready ? "Selected period: " + start.value + " to " + end.value : "";
  };
  const clearDates = () => {
    start.value = "";
    end.value = "";
    choosingEnd = false;
    updateSelection();
  };
  function refreshPeriods() {
    clearDates();
    calendar.hidden = true;
    period.replaceChildren(new Option(staff.value ? "Select a period" : "Select staff first", ""));
    period.disabled = !staff.value;
    if (!staff.value) return;
    const options = new Map();
    for (const run of runs) options.set(run.start + "|" + run.end, {start: run.start, end: run.end, label: run.label + " · " + run.start + " to " + run.end});
    const now = new Date();
    for (let offset = 3; offset >= -24; offset--) {
      const first = new Date(now.getFullYear(), now.getMonth() + offset, 1);
      const last = new Date(first.getFullYear(), first.getMonth() + 1, 0);
      const a = iso(first), b = iso(last);
      if (runs.some(run => overlaps(a, b, run) && (run.start !== a || run.end !== b))) continue;
      const key = a + "|" + b;
      if (!options.has(key)) options.set(key, {start: a, end: b, label: first.toLocaleDateString(undefined, {month: "long", year: "numeric"})});
    }
    for (const item of [...options.values()].sort((a, b) => b.start.localeCompare(a.start))) {
      const blocked = unavailable(item.start, item.end);
      const suffix = blocked ? (blocked.status === "paid" ? " — Already paid" : ["approved", "closed"].includes(blocked.status) && !existingSalary(item.start, item.end) ? " — Period locked" : " — Already generated; review below") : "";
      const option = new Option(item.label + suffix, item.start + "|" + item.end);
      option.disabled = Boolean(blocked);
      period.add(option);
    }
    period.add(new Option("Choose a custom date range…", "custom"));
  }
  function renderCalendar() {
    document.getElementById("salary-calendar-month").textContent = month.toLocaleDateString(undefined, {month: "long", year: "numeric"});
    document.getElementById("salary-calendar-instruction").textContent = choosingEnd ? "Start: " + start.value + ". Select an end date." : "Select a start date, then an end date.";
    days.replaceChildren();
    const year = month.getFullYear(), m = month.getMonth();
    for (let i = 0; i < new Date(year, m, 1).getDay(); i++) days.append(document.createElement("span"));
    for (let day = 1; day <= new Date(year, m + 1, 0).getDate(); day++) {
      const date = iso(new Date(year, m, day));
      const button = document.createElement("button");
      button.type = "button";
      button.textContent = day;
      button.setAttribute("aria-label", date);
      button.disabled = Boolean(unavailable(date, date) || (choosingEnd && (date < start.value || unavailable(start.value, date))));
      const selected = date === start.value || date === end.value || (start.value && end.value && date > start.value && date < end.value);
      button.setAttribute("aria-pressed", String(Boolean(selected)));
      button.className = "rounded-lg p-2 text-sm border border-slate/10 disabled:opacity-30 disabled:cursor-not-allowed " + (selected ? "bg-obsidian text-white" : "bg-white hover:bg-aqua/20");
      if (button.disabled) button.title = "Unavailable for this salary period";
      button.addEventListener("click", () => {
        if (!choosingEnd) {
          start.value = date;
          end.value = "";
          choosingEnd = true;
        } else {
          end.value = date;
          choosingEnd = false;
        }
        updateSelection();
        renderCalendar();
      });
      days.append(button);
    }
  }
  staff.addEventListener("change", refreshPeriods);
  period.addEventListener("change", () => {
    clearDates();
    calendar.hidden = period.value !== "custom";
    if (period.value === "custom") {
      month = new Date();
      renderCalendar();
    } else if (period.value) {
      [start.value, end.value] = period.value.split("|");
      updateSelection();
    }
  });
  document.getElementById("salary-month-previous").addEventListener("click", () => {
    month = new Date(month.getFullYear(), month.getMonth() - 1, 1); renderCalendar();
  });
  document.getElementById("salary-month-next").addEventListener("click", () => {
    month = new Date(month.getFullYear(), month.getMonth() + 1, 1); renderCalendar();
  });
  document.getElementById("salary-calendar-reset").addEventListener("click", () => { clearDates(); renderCalendar(); });
  form.addEventListener("submit", event => {
    if (!start.value || !end.value || unavailable(start.value, end.value)) {
      event.preventDefault();
      selection.textContent = "Select an available period before generating salary.";
    }
  });
  refreshPeriods();
})();
