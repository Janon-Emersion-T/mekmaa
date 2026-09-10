(() => {
  "use strict";
  const form = document.getElementById("staff-salary-form");
  if (!form) return;
  const staff = document.getElementById("salary-staff");
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
    const ready = staff.value && start.value && end.value && end.value >= start.value && !unavailable(start.value, end.value);
    generate.disabled = !ready;
    selection.textContent = ready ? "Selected period: " + start.value + " to " + end.value : "";
  };
  const clearDates = () => {
    start.value = "";
    end.value = "";
    choosingEnd = false;
    updateSelection();
  };
  function closeCalendar() {
    calendar.hidden = true;
    start.setAttribute("aria-expanded", "false");
    end.setAttribute("aria-expanded", "false");
  }
  function refreshStaff() {
    clearDates();
    closeCalendar();
    start.disabled = !staff.value;
    end.disabled = true;
  }
  function openCalendar(forEnd) {
    if (!staff.value || (forEnd && !start.value)) return;
    choosingEnd = forEnd;
    const date = forEnd ? (end.value || start.value) : start.value;
    month = date ? new Date(Number(date.slice(0, 4)), Number(date.slice(5, 7)) - 1, 1) : new Date();
    calendar.hidden = false;
    start.setAttribute("aria-expanded", String(!forEnd));
    end.setAttribute("aria-expanded", String(forEnd));
    renderCalendar();
  }
  function renderCalendar() {
    document.getElementById("salary-calendar-month").textContent = month.toLocaleDateString(undefined, {month: "long", year: "numeric"});
    document.getElementById("salary-calendar-instruction").textContent = choosingEnd ? "From: " + start.value + ". Choose the to date." : "Choose the from date.";
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
          end.disabled = false;
          start.setAttribute("aria-expanded", "false");
          end.setAttribute("aria-expanded", "true");
        } else {
          end.value = date;
          choosingEnd = false;
          closeCalendar();
          end.focus();
        }
        updateSelection();
        renderCalendar();
      });
      days.append(button);
    }
  }
  staff.addEventListener("change", refreshStaff);
  for (const input of [start, end]) {
    input.addEventListener("click", () => openCalendar(input === end));
    input.addEventListener("keydown", event => {
      if (["Enter", " ", "ArrowDown"].includes(event.key)) {
        event.preventDefault();
        openCalendar(input === end);
        days.querySelector("button:not(:disabled)")?.focus();
      }
    });
  }
  calendar.addEventListener("keydown", event => {
    if (event.key === "Escape") {
      closeCalendar();
      (choosingEnd ? end : start).focus();
    }
  });
  document.getElementById("salary-month-previous").addEventListener("click", () => {
    month = new Date(month.getFullYear(), month.getMonth() - 1, 1); renderCalendar();
  });
  document.getElementById("salary-month-next").addEventListener("click", () => {
    month = new Date(month.getFullYear(), month.getMonth() + 1, 1); renderCalendar();
  });
  document.getElementById("salary-calendar-reset").addEventListener("click", () => { clearDates(); end.disabled = true; openCalendar(false); });
  form.addEventListener("submit", event => {
    if (!staff.value || !start.value || !end.value || end.value < start.value || unavailable(start.value, end.value)) {
      event.preventDefault();
      selection.textContent = "Select an available period before generating salary.";
    }
  });
  refreshStaff();
})();
