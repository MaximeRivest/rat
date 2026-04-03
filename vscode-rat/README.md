# Rat — VS Code Extension

Run code cells in Markdown (`.md`) and Quarto (`.qmd`) files, powered by [rat](https://github.com/maximerivest/rat) kernels.

Works **on top of** whatever Markdown / Quarto extension you already use — it only adds code execution.

## Features

| Feature | How |
|---------|-----|
| **Run cells** | `▶ Run` CodeLens above each cell, or `Ctrl+Enter` |
| **Output cells** | Results appear in ` ```output ``` ` blocks below |
| **Plots** | Matplotlib PNGs saved to `_assets/`, inserted as images |
| **Execution queue** | Cells queue up, execute in order |
| **Pause / Cancel** | Status bar toggle, `Ctrl+Shift+C` to cancel |
| **Completions** | Live from kernel — sees your DataFrames, imports, etc. |
| **Hover** | Rich inspection on hover (type, shape, docs, methods) |
| **stdin prompts** | `input()` pops a VS Code input box |
| **Run Above / Run All** | Execute cells from top, or the whole file |

## Prerequisites

```bash
# Install rat
curl -fsSL https://runanything.dev/install.sh | sh
rat install py    # or sh, r, ju, js
```

## Install the extension

```bash
cd vscode-rat
npm install
npm run build

# Option A: development mode
# Open this folder in VS Code, press F5

# Option B: install globally
npm run package
code --install-extension rat-0.1.0.vsix
```

## Usage

Open any `.md` or `.qmd` file with fenced code blocks:

````markdown
```python
import pandas as pd
df = pd.read_csv("data.csv")
df.head()
```
````

Click **▶ Run** above the cell. Output appears below:

````markdown
```output
   region quarter  revenue
0   East      Q1    42000
1   West      Q1    38000

✓ 150ms | 2 vars
```
````

### Keybindings

| Key | Action |
|-----|--------|
| `Ctrl+Enter` | Run cell at cursor |
| `Shift+Enter` | Run cell and advance to next |
| `Ctrl+Shift+Enter` | Run all cells above (inclusive) |
| `Ctrl+Shift+C` | Cancel execution |

### Runtime resolution

The extension decides which rat kernel to use:

1. **Front matter** — per-file override:
   ```yaml
   ---
   rat:
     python: py-ml
   ---
   ```

2. **VS Code setting** — per-workspace:
   ```json
   { "rat.runtimes": { "python": "py-ml" } }
   ```

3. **Default** — canonical language name (`py`, `sh`, `r`, …)

CWD is always the VS Code workspace folder.

### Plots

When your code calls `plt.show()`, the figure is saved as a PNG
in `_assets/` (configurable via `rat.assetsDir`) and inserted as
a Markdown image after the output block:

```markdown
![plot](_assets/fig-1234567890-0.png)
```

## Settings

| Setting | Default | Description |
|---------|---------|-------------|
| `rat.path` | `"rat"` | Path to the rat binary |
| `rat.runtimes` | `{}` | Map fence language → named runtime |
| `rat.maxOutputLines` | `100` | Max lines in output cells (0 = unlimited) |
| `rat.assetsDir` | `"_assets"` | Plot image directory (relative to workspace) |

## Commands

All available via the Command Palette (`Ctrl+Shift+P`):

- **Rat: Run Cell** / **Run Cell and Advance** / **Run Above** / **Run All Cells**
- **Rat: Cancel Execution** / **Clear Queue** / **Pause / Resume Queue**
- **Rat: Clear All Outputs** — remove all ` ```output ``` ` blocks
- **Rat: Show Variables** — open variable overview in a side panel
- **Rat: Stop Kernel** / **Restart Kernel**
