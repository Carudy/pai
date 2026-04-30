# Question Answering — QA Agent

Get direct answers to your questions
```bash
# Single turn
pai --agent qa "what is recursion"
pai -a qa "explain Go goroutines in one sentence"
```

or start an interactive multi-turn session
```bash
# Interactive multi-turn chat (full TUI with scrolling)
pai -a qa -i "help me understand Kubernetes pods"
# Or without any init input
pai -a qa -i
```
