# Experiments & Data Tasks — Examples

Use PAI for quick data processing, scripting, and ad-hoc experiments.

---

### Example: Generate data

```bash
$ pai -a cmd "randomly generate a 5x5 grid of numbers and write to data.csv"
```

```
[CMD 💬] Generate a 5x5 grid of random integers (0-99) as CSV, write to data.csv.
[CMD 💻] awk 'BEGIN{srand(); for(i=1;i<=5;i++){for(j=1;j<=5;j++) printf "%s%d", (j==1?"":","), int(rand()*100); print ""}}' > data.csv
Execute this command?
[*] Yes
[ ] No
(Press ↑/↓ and Enter to choose; q or ctrl+c to quit.)
[SYS] ✅ Command succeeded
[RES]
[exit 0]
```

---

### Example: Multi-step scripting (devops mode)

```bash
$ pai "sum numbers in the 2nd column of data.csv, output sum/2, and if correct save the script to calc.sh"
```

```
[PAI 🤖] Let's check the file exists and inspect its format first.
────────────────────────────────────────
[CMD 💬] Check that data.csv exists.
[CMD 💻] test -f data.csv && echo 'exists' || echo 'not found'
[SYS] Command succeeded
[CMD Result]
exists
────────────────────────────────────────
[token: 234 in, 45 out, 279 total]

[PAI 🤖] File exists. Let's peek at the first few lines.
────────────────────────────────────────
[CMD 💬] Inspect first lines to confirm CSV format.
[CMD 💻] head -3 data.csv
[SYS] Command succeeded
[CMD Result]
18,13,88,74,38
77,58,60,50,40
24,9,14,37,77
────────────────────────────────────────
[token: 589 in, 112 out, 701 total]

[PAI 🤖] Confirmed 5-column CSV. Now calculate sum of column 2 ÷ 2.
────────────────────────────────────────
[CMD 💬] Sum the second column and divide by 2.
[CMD 💻] awk -F',' '{sum+=$2} END {print sum/2}' data.csv
[SYS] Command succeeded
[CMD Result]
101.5
────────────────────────────────────────
[token: 445 in, 98 out, 543 total]

[PAI 🤖] Result is 101.5. Now save the script to calc.sh.
────────────────────────────────────────
[CMD 💬] Save the awk one-liner as calc.sh.
[CMD 💻] cat > calc.sh << 'EOF'
awk -F',' '{sum+=$2} END {print sum/2}' data.csv
EOF
[SYS] Command succeeded
────────────────────────────────────────
[token: 390 in, 87 out, 477 total]

[PAI ✅] Script saved to calc.sh. Run with: bash calc.sh → outputs 101.5.
```
