---
name: binary-size
description: Analyze fir binary size per-package, identify bloat contributors, and check the size guard baseline.
override: true
---

# Binary Size Analysis

Run the per-package function text analyzer:

```bash
make build
go run <skill-path>/binsize.go bin/fir
```

Report the table. Flag any surprising or new large entries vs the known baselines below.
