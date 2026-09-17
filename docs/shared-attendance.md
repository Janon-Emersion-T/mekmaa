# Shared attendance entry

Share `/attendance` (for production, `https://mekmaa.com/attendance`). The browser asks for the dedicated attendance username and password. The default username is `attendance`; the password is the one agreed with the administrator. This credential is accepted only by `/attendance` and `/attendance/save` and does not create an admin session.

The admin Attendance page includes **Open shared attendance** and **Copy attendance link** buttons.

The recipient selects a course, group, active session, and attendance date. Courses from every division are available. The session weekday must match the selected date. Each student needs a status before the sheet can be saved; **Mark all present** can be used before changing exceptions.

Existing sheets are locked on the shared page, including sheets entered by administrators. Repeat submissions are rejected on the server. Administrators can still make corrections through the existing admin attendance page.

To change the shared credentials, set `ATTENDANCE_USERNAME` and `ATTENDANCE_PASSWORD` in the application's environment and restart the application. The default password is stored as a bcrypt hash in source. No database migration is required.
