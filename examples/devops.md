# DevOps, pai can save 90% of you time.

### Example: checking some services's status
```bash
$ pai "find all frpc's port and usage"
```
    > [PAI 🤔] We need to find all frpc (frp client) processes and their port mappings and usage. 
    frpc is typically configured via a .toml or .ini file. 
    We can look for running frpc processes, find their config files, 
    and parse the port mappings. Also note the working directory and user.
    
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
    
    [PAI 🤔] We see that frpc is running as root with a config file at /path/to/frpc.toml. 
    To find ports and usage, we need to read that config file.
    ────────────────────────────────────────
    [CMD 💬] Read the frpc config file to see the port forwarding rules and usage.
    [CMD 💻] cat /path/to/frpc.toml
    Execute this command?
    [*] Yes
    [ ] No
    (Press ↑/↓ and Enter to choose; q or ctrl+c to quit.)
    [SYS] Command succeeded
    [CMD Result]
    cat: /path/to/frpc.toml: Permission denied
    
    ────────────────────────────────────────
    
    [PAI 🤔] We need to run with sudo or as root. Since user is dy, not root, 
    but we can use sudo. The process runs as root, 
    so we likely need sudo to read config. Let's try with sudo.
    ────────────────────────────────────────
    [CMD 💬] Need sudo to read root-owned config file.
    [CMD 💻] sudo cat /path/to/frpc.toml
    Execute this command?
    [*] Yes
    [ ] No
    (Press ↑/↓ and Enter to choose; q or ctrl+c to quit.)
    [sudo] password for dy: [REDACTED]
    [SYS] Command succeeded
    [CMD Result]
    
    [REDACTED CONFIG CONTENT]
    
    ────────────────────────────────────────
    
    [PAI 🤔] We have the frpc config. It lists proxies with names, localIP, localPort, and remotePort. 
    The user asked "find all frpc's port and usage". 
    That likely means list the ports (both local and remote) 
    and what each is used for (the name gives usage). 
    We can present it in a structured way. 
    Since we have the info, we can output a summary with "done".
    ────────────────────────────────────────
    [PAI ✅] Found frpc running on server example.com:7000. Proxies:
    - ssh1: local port 22 → remote 12222
    - web: local port 80 → remote 8080
    [OTHER MAPPINGS]

### Example: auto check & commit & push, etc. 

```bash
$ pai "in this repo, check changes, make a commit; then rm all old tags, make a new tag 0.3.1; then push to both orgin and the new tag"
```

*We ignore the long-interaction.*
