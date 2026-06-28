# Izen UI/UX Design Specification

> Manifesto: "Engineering intelligence. Human in control."
izen is an ambient, high-performance developer companion. It respects user focus, integrates into existing terminal multiplexers (Tmux/Zsh), and prioritizes physical-like tactile responsiveness over complex window frameworks.

1. Core Architectural Shift: The Classic CLI/TUI Hybrid

To prevent text clipping, scrollback loss, and display framing bugs common in complex terminal interfaces, izen rejects the Fullscreen AltScreen Mode (tea.WithAltScreen()). Instead, it operates in Standard Output (Classic CLI) mode.

How it Works:

The Stage (Native Scrollback Buffer): All historical chat text, streamed code blocks, and operation results are printed directly to stdout. The native terminal emulator controls the buffer. The user can scroll up naturally using their mouse or terminal bindings to search or copy old chats.

The Prompt (Anchored Dynamic Input): Bubble Tea controls only the bottom 3 lines of the screen (the interactive prompt). Every keystroke updates just this active zone, reducing the redraw layout to a sub-10ms footprint, guaranteeing zero input lag and 60 FPS performance.

2. Visual Style & Ambient Interface

To prevent visual fatigue, color is treated as a premium resource. The main text retains the system's native terminal foreground (mostly Latte White/Muted Gray). Mode changes are communicated elegantly through two continuous horizontal lines.

Color Guide (Catppuccin Mocha Accents)

Only the parallel horizontal lines, the dynamic prompt prefix, and the active cursor reflect the active mode's color:

Mode

Theme Accent

Tone Name

HEX Equivalent

ANSI Code

/ask

Safe, Read-Only

Mint Green

#a6e3a1

Green (10)

/plan

Architecture, Prep

Amber Orange

#fab387

Yellow (11)

/build

Creative, Mutation

Sapphire Blue

#89b4fa

Blue (12)

/investigate

Deep Debugging

Orchid Purple

#cba6f7

Magenta (13)

/review

Audit & Verification

Lemon Yellow

#f9e2af

Yellow (3)

Neutral

System Lines

Charcoal Muted

#313244

Dark Gray (8)

Meta

Dimmed Status

Slate Dim

#585b70

Gray (240)

Strict Icon Philosophy: "Value over Decoration"

No Icon Bloat: Do not prepend generic icons to files (📁 main.go) or prompts.

Semantic States Only:

✔ (Mint Green): Successful operations.

✕ (Coral Red): Errors or exceptions.

❯ (Muted Gray): Floating option selector.

⚒ (Sapphire Blue): Write capabilities activated (Build/Investigate).

3. Structural Layout: Parallel Focus Lines

The active interface occupies a dedicated bottom anchor space, separated by two parallel unicode lines (─) that span the exact width of the terminal.

(Historical chat flow pushed up naturally into native terminal scrollback...)
│
│  you > write a simple HTTP handler in Go
│  izen (250ms) 
│  ├── cmd/server/main.go
│  ✔ done • 120/32k tokens • $0.00
│
[Active Mode Color] ─────────────────────────────────────────────────────────────────── [Line 1]
build> _                                                                                [Prompt]
[Active Mode Color] ─────────────────────────────────────────────────────────────────── [Line 2]


Prompt Jump Mitigation (Anti-Jump Lock)

When rendering long code chunks or outputting streaming tokens, the prompt line can jump or get pushed off-screen.

The Solution: Maintain a window coordinate tracker. If the viewport shifts abnormally or terminal resize occurs, pressing Enter instantly flushes the ongoing write-output, clears the active input line, and positions the con cursor back to the fixed bottom layout using ANSI cursor control coordinates (\033[u and \033[H).

4. Ergonomic Keyboard Routing & History Navigation

Interactive input must support standard shell keybindings without conflicting with the native terminal scrolling.

Horizontal Arrows (Left / Right):

Handled strictly by the textinput component to move the cursor within the current text buffer to support fast inline edits.

Vertical Arrows (Up / Down):

Routed to the Command History Buffer (similar to Zsh/Fish).

Pressing Up fetches the previous prompt.

Pressing Down navigates toward more recent prompts, clearing at the bottom-most boundary.

No Scroll Conflict: The arrow keys do not attempt to scroll the page. This leaves the terminal emulator’s scroll wheel/trackpad gestures completely unobstructed.

5. Ambient Micro-Interactions & Fluid Animations

To achieve a tactile, professional software feel, we implement smooth frame-pacing and linear color transitions.

A. Color-Interpolated Mode Transitions

When switching modes (e.g., from /ask to /build), the parallel lines do not instantly snap to the new color.

Mechanism: On mode transition, trigger a 150ms animation loop ticking at 25ms (~40 FPS). The color of the lines smoothly interpolates (fades) through dark tones (Charcoal Muted -> Slate Dim) before expanding back to the target mode's bright accent.

B. Liquid Streaming via Token Buffering

Standard LLM token generation (especially running on local Ollama) can stutter, causing jarring text jumps.

Mechanism:

Incoming tokens from the model channel are pushed into a First-In-First-Out (FIFO) Token Queue.

A fast frame loop (tea.Tick at 16ms / 60 FPS) pulls from the queue and renders characters to the terminal at an organically smoothed rate.

If inference stutters, the user sees a smooth, continuous typewriter effect.

C. The "Breathing" Solid Cursor

Streaming: A solid white cursor block (█) follows the streamed text tightly to draw the eye downward.

Idle: The cursor transitions into a slow-pulsing "breathing" state. It cycles through ANSI grayscale shades (█ -> ▒ -> ░ ->  ) with an 800ms period, signaling the system is waiting for human command without flashing aggressively.

6. Phased Implementation Plan for AI Agents

To ensure risk-free, modular execution, the refactoring of ui/ must progress in four distinct, testable phases.

### graph TD
    Phase1[Phase 1: Hybrid CLI Render Engine] --> Phase2[Phase 2: Color Lines & Prompt Lock]
    Phase2 --> Phase3[Phase 3: Arrow Bindings & History]
    Phase3 --> Phase4[Phase 4: Interpolations & Streaming Polish]


## Phase 1: Hybrid CLI Render Engine (Standard Output)

Goal: Strip full-screen viewport wrappers and redirect output to standard terminal streaming.

Action Items:

Disable tea.WithAltScreen() in ui/program.go.

Ensure the main conversation model prints results via standard standard output.

Limit the View() function of the prompt model to output exactly the 3 lines (Line 1, Input Line, Line 2).

## Phase 2: Dual Color Lines & Anti-Jump Lock

Goal: Render the dual focus borders and anchor the input prompt line.

Action Items:

Implement dynamic horizontal line drawing using terminal width from tea.WindowSizeMsg.

Attach Mode State to the line color style.

Apply ANSI Escape sequence commands in View() to prevent prompt line jumps and handle auto-centering upon pressing Enter.

## Phase 3: Ergonomic Navigation & Command History

Goal: Establish smooth, lag-free command prompt history traversal.

Action Items:

Configure Left/Right arrow key event routing to prevent Bubble Tea bubble-up bubbling.

Implement a simple slice-based history manager in ui/model.go to store successfully entered commands.

Bind Up/Down keys to traverse the command history and set the cursor at the end of the line on selection.

## Phase 4: Color Fade, Breathing Cursor & Queue-based Stream

Goal: Polish transitions and streaming aesthetics to achieve professional liquid feedback.

Action Items:

Implement the 25ms tea.Tick animation loop for line color transitions.

Code the token queue buffer structure inside ui/stream.go to smooth local LLM inference stutters.

Build the idle gray-scale pulsing cursor █ component.
