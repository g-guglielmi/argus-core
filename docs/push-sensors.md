# Push sensors

A push sensor watches a job that runs on its own schedule: a backup, a cron job, a scheduled task, a
script on a NAS. Instead of Argus checking the job, the job tells Argus when it ran, by calling the
push sensor's own URL at the end of the run. Argus then knows three things:

- the run **failed**: the job said so (an error, with the job's message as the reason);
- the job is **late**: no run for longer than you expect (a warning);
- the job **didn't run**: no run for much longer than that (an error).

A push sensor belongs to a host, the machine the job runs on or the one it looks after, and shows up
in that host's sensor list under **Push**: how long ago the job last ran, or **failed** with its
message. Like every sensor it has a history, an uptime, maintenance windows and the host's master
sensor, and its alerts go to the same channels.

## Add one

Open the host's **Settings** and find **Push sensors** (admin and helpdesk):

1. **+ Add push sensor**, and give it a name (`Nightly backup`).
2. Set **Warning when no run for** and **Error when no run for**. For a daily job, 25 and 49 hours
   give it an hour of slack before each; for an hourly one, 70 minutes and 3 hours.
3. **Add push sensor**. Its URL shows right away, with examples to copy. The sensors appear on the
   host within a minute.

Changes in this section are saved at once; the host settings' **Save** isn't needed for them.

Argus needs to know its own address for this, the **Public URL** in Settings: the host's probe reads
the push sensors from it (see [How it works](#how-it-works)).

## Call it from the job

Call the URL at the end of the run, with `status=ok` or `status=fail` and, if you like, a `msg` (up to
200 characters, shown as the reason of a failed run). Plain `GET` and `POST` both work; for `POST`
the fields can also be a form or a JSON body.

Linux, macOS, a NAS (anything with curl):

```sh
# at the end of the job
curl -fsS -m 10 --retry 3 "https://monitoring.example.com/api/push/<token>?status=ok&msg=Backup+done"
# ...or when it went wrong
curl -fsS -m 10 --retry 3 "https://monitoring.example.com/api/push/<token>?status=fail&msg=Backup+failed"
```

A wrapper that reports the job's own result, with its exit code as the message on a failure:

```sh
if /usr/local/bin/backup.sh; then
  curl -fsS -m 10 --retry 3 "https://monitoring.example.com/api/push/<token>?status=ok"
else
  curl -fsS -m 10 --retry 3 "https://monitoring.example.com/api/push/<token>?status=fail&msg=exit+code+$?"
fi
```

Windows (PowerShell, also from a scheduled task):

```powershell
Invoke-RestMethod -Method Post -Uri "https://monitoring.example.com/api/push/<token>" -Body @{ status = 'ok'; msg = 'Backup done' }
```

JSON, from tools that send it:

```sh
curl -fsS -m 10 -H "Content-Type: application/json" -d '{"status":"fail","msg":"disk full"}' "https://monitoring.example.com/api/push/<token>"
```

`status` also accepts `up` and `down`, so a job written for an Uptime Kuma push monitor works as it
is. A blank status counts as a success. Argus answers `{"ok":true}`, or `404` for an unknown URL and
`400` for a status it doesn't know.

## Keep the URL private

Anyone with the URL can report runs for that job, so keep it in the job's own settings, not in a
shared document. **New URL** (the push sensor's menu) replaces it; the old one stops working at once.
Admins and helpdesk see the URLs; viewers see only the push sensors and their last run.

## How it works

Argus keeps each push sensor's last run. The host gets the **Argus Push** template (Argus links it with
the first push sensor and unlinks it with the last; you don't attach it by hand), which reads the
host's push sensors back from Argus once a minute, run by the host's own probe (or the core, for a host
the core monitors). So a run shows up within a minute, and the probe must reach Argus at its Public URL
over a certificate the probe trusts, the same way it already does for enrollment and check-ins.

Each push sensor becomes three items: the last run's result (`OK` or `Failed`), the time since it
(before the first run, the time since the push sensor was made, so a job that never starts is caught
too) and its message. If the probe can't read Argus, each push sensor shows why (no answer, a refused
key, something that isn't Argus answering) instead of going quiet.

A run that reports a failure is a **High** alert, cleared by the next successful run. The late time
raises a **Warning**, the missed time a **High** alert; both clear when the job runs again. The times
are set per push sensor, since every job has its own schedule.

When two runs arrive within the same minute, the later one is what the sensor shows.

Deleting a push sensor stops its URL at once; its sensors stop at the next read and are removed, with
their history, within the hour (at once when it was the host's last push sensor, since the template
goes with it).
