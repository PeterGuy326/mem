# Demo images — sources & licenses

Real photos used to demo image search (CLIP visual route). All from Wikimedia
Commons, downscaled to ~640px for size. Replace with your own photos any time —
just `bin/mem put <your.jpg>` (see docs/RUN_LOCAL.md).

| file | subject | source (Wikimedia Commons) | license |
|------|---------|----------------------------|---------|
| `golden_retriever_grass.jpg` | golden retriever on grass (killer demo) | `File:Golden Retriever Carlos (10581910556).jpg` | CC BY 2.0 |
| `cat.jpg` | domestic cat | `File:Cat03.jpg` | CC BY-SA 3.0 |
| `river_landscape.jpg` | river / nature landscape (Lahemaa, Estonia) | `File:Altja jõgi Lahemaal.jpg` | CC BY-SA 3.0 |

These give clear cross-modal discrimination (dog vs cat vs landscape) so the
visual-route assertion in `seed_demo_data.sh` (Q4) is meaningful.
