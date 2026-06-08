# Private Agent — Examples

The private agent extracts math expressions from user input, resolves masked placeholders
(`<mask:abc>`) from `~/.config/pai/mask.toml`, and computes results via Python.

---

### Example: Arithmetic with masks

```bash
$ pai -a private "what is <mask:abc> to the power of <mask:xyz> plus 10"
```

```
[CALC 💬] math calculation detected
[CALC 📐] <mask:abc> ** <mask:xyz> + 10
  → 2 ** 3.14 + 10
Execute this command?
[*] Yes
[ ] No
[RES]
19.869176983372224
```

---

### Example: Comparison

```bash
$ pai -a private "is <mask:abc> bigger than the square of <mask:xyz>"
```

```
[CALC 💬] math calculation detected
[CALC 📐] <mask:abc> >= <mask:xyz> ** 2
  → 2 >= 3.14 ** 2
[RES]
False
```

---

### Example: No math — info response

```bash
$ pai -a private "what is pi"
```

```
[PAI 🤖] math calculation not detected
Pi is the mathematical constant representing the ratio of a circle's circumference
to its diameter, approximately equal to 3.14159.
```
