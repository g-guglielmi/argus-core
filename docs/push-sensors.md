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

## Report by email

Some gear can't call a URL but mails its result: a backup server's job report, a NAS's daily report,
an appliance's digest. A push sensor can read its runs from those mails instead (or as well).

**First, a mailbox** (Settings, Monitoring, **Mailboxes**; admins): one that only these reports reach,
like `backups@example.com`. Argus reads it every 2 minutes and never changes it: no mail is marked
read, moved or deleted. **Test** signs in and says how many mails the folder holds.

- **IMAP**: the server, its port and TLS or STARTTLS (993 and 143 by default), the user name and
  password (an app password where the provider has them), and the folder (`INBOX`). The server's
  certificate must be one a public authority signed.
- **Microsoft 365**: Exchange Online doesn't take passwords over IMAP, so Argus reads the mailbox
  through Microsoft Graph as an app that may read only that one mailbox. In the Microsoft Entra admin
  center, **App registrations**, **New registration** (`Argus mail reader`, single tenant), then
  **Certificates & secrets**, **New client secret**; note the application (client) ID, the directory
  (tenant) ID and the secret. Then, in Exchange Online PowerShell, let the app read only that mailbox
  (RBAC for Applications; don't add Mail.Read under the app's API permissions, which would let it
  read every mailbox):

  ```powershell
  New-ServicePrincipal -AppId <client id> -ObjectId <the enterprise app's object id> -DisplayName "Argus mail reader"
  New-ManagementScope -Name "Argus mailboxes" -RecipientRestrictionFilter "PrimarySmtpAddress -eq 'backups@example.com'"
  New-ManagementRoleAssignment -App <client id> -Role "Application Mail.Read" -CustomResourceScope "Argus mailboxes"
  Test-ServicePrincipalAuthorization -Identity <client id> -Resource backups@example.com
  ```

  The enterprise app's object id is under **Enterprise applications**, not the app registration. Give
  Argus the tenant ID, the client ID, the secret, the mailbox's address and the folder (`Inbox`, or a
  folder's name at the mailbox's top level). Secrets expire: make a new one before, and paste it in.

**Then, the push sensor**: in its editor, **Read its runs from email**, the mailbox, and what its job's
mails look like: **From (contains)** (`veeam@example.com`) and **Subject contains** (`Backup job NAS`),
one or both. **Success when it says** and **Failure when it says** are words, separated by commas
(`success, succeeded, completed` and `fail, error` to start with). Argus looks for them in the subject,
then in the start of the text, ignoring case, where a word starts (`fail` finds `Failed`, `ok` doesn't
find `book`); a failure word wins over a success word. A matching mail that says neither is a failed
run, with its subject as the reason, so a vendor rewording its mails doesn't pass quietly. A run counts
at the time its mail arrived, so a mail read a few minutes later doesn't shift it; one that arrives
late (older than the run on record) is counted but doesn't replace it. A rule saved after its job's
mail arrived still finds it, from the last two days.

When a mailbox can't be read, Settings says why, and its push sensors that are late say "No run read:
its mailbox ... can't be read since ...", so the alert names the real trouble.

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
