# DevOps Agent — Examples

The devops agent runs an autonomous reason→act→observe loop. It inspects output, adapts to errors, and drives toward the goal step by step.

---

### Example: Checking service status

```bash
$ pai "find all frpc's port and usage"
```

```
[PAI 🤖] We need to find all frpc processes and their port mappings.
         Let's start by checking if frpc is running and locate its config.
────────────────────────────────────────
[CMD 💬] Check if frpc processes are running and find their command lines.
[CMD 💻] ps aux | grep frpc
Execute this command?
[*] Yes
[ ] No
(Press ↑/↓ and Enter to choose; q or ctrl+c to quit.)
[SYS] Command succeeded
[CMD Result]
root     12345  0.0  0.1  123456  7890 ?        S    Apr29   0:00 /path/to/frpc -c /path/to/frpc.toml
user     23456  0.0  0.0   12345   1234 pts/0    S+   17:30   0:00 grep --color=auto frpc
────────────────────────────────────────
[token: 856 in, 153 out, 1K total]

[PAI 🤖] frpc is running as root with a config file at /path/to/frpc.toml.
         To find ports, we need to read that config file.
────────────────────────────────────────
[CMD 💬] Read the frpc config file to see port forwarding rules.
[CMD 💻] sudo cat /path/to/frpc.toml
Execute this command?
[*] Yes
[ ] No
[SYS] Command succeeded
[CMD Result]
[REDACTED CONFIG CONTENT]
────────────────────────────────────────
[token: 1.1K in, 218 out, 1.3K total]

[PAI ✅] Found frpc running on server example.com:7000. Proxies:
- ssh1: local port 22 → remote 12222
- web: local port 80 → remote 8080
```

---

### Example: Remote host management

```bash
$ pai "check nginx status on webserver and show the last 5 error log lines"
```

*Requires `webserver` to be a Host alias in `~/.ssh/config`.*

```
[PAI 🤖] First let's check what hosts are available.
────────────────────────────────────────
[CMD 💬] List available SSH hosts from config.
[CMD 💻] cat ~/.ssh/config | grep "^Host "
Execute this command?
[*] Yes
[ ] No
[SYS] Command succeeded
[CMD Result]
Host webserver
Host dbserver
────────────────────────────────────────
[token: 412 in, 89 out, 501 total]

[PAI 🤖] Found webserver. Let's check nginx status on it.
────────────────────────────────────────
[RMT 💬] Check if nginx is running on webserver.
[RMT 💻 @webserver] systemctl status nginx
Run on webserver?
[*] Yes
[ ] No
[SYS] Remote command succeeded
[CMD Result]
● nginx.service - A high performance web server
   Loaded: loaded (/lib/systemd/system/nginx.service; enabled)
   Active: active (running) since Mon 2025-01-06 10:30:00 UTC
────────────────────────────────────────
[token: 567 in, 134 out, 701 total]

[PAI 🤖] Nginx is running. Now let's check the error log.
────────────────────────────────────────
[RMT 💬] Show last 5 lines of nginx error log.
[RMT 💻 @webserver] tail -n 5 /var/log/nginx/error.log
[SYS] Remote command succeeded
[CMD Result]
2025/01/06 10:31:22 [warn] 12345#12345: *1024 upstream server temporarily disabled
2025/01/06 10:32:05 [error] 12345#12345: *1028 connect() failed (111: Connection refused)
...
────────────────────────────────────────
[token: 623 in, 156 out, 779 total]

[PAI ✅] Nginx is running on webserver. Recent errors include one upstream
         temporarily disabled and one connection refused at 10:32:05 UTC.
```

Connections to `webserver` are cached — subsequent remote commands reuse the same SSH session (no re-auth).

---

### Example: Auto commit, tag, and push

```bash
$ pai "in this repo, check changes, make a commit; then remove old tags, create tag v0.3.1; push to origin and the new tag"
```

*Long interaction omitted — the agent checks `git status`, stages changes, commits, deletes old tags, creates the new one, and pushes.*
