# Module photo-to-trace 

Captures photos from a camera — optionally saving a second copy to a cloud-sync folder — and turns them into CSVs of draw-ordered points, either by tracing the outlines of colored shapes on a white background or, for portraits, by running the [portrait-outliner](../portrait-outliner/README.md) Python program.

## Models

This module provides the following model(s):

- [`6d6c7293bc6743e49c8b31c76aac27d3:photo-to-trace:outliner`](6d6c7293bc6743e49c8b31c76aac27d3_photo-to-trace_outliner.md) - Captures camera frames (`capture`), traces shape outlines (`trace`), outlines portraits (`outline`), does the whole photograph-and-outline pipeline in one call (`portrait`), forwards a finished CSV to a configured plotter (`draw`), and does capture-outline-draw end to end (`draw_portrait`).
