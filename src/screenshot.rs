//! `--screenshot FILE.png`: draws what dev prints into a terminal-style picture.
//! dev is a console program, so the picture is its own output rendered with an 8x8 font.

use crate::util::*;
use font8x8::{UnicodeFonts, BASIC_FONTS};
use std::fs::File;
use std::io::BufWriter;
use std::path::Path;

const SCALE: usize = 2;
const CELL_W: usize = 8 * SCALE;
const CELL_H: usize = 8 * SCALE + 6;
const COLS: usize = 92;
const PAD: usize = 18;
const BAR: usize = 34;

type Rgb = [u8; 3];
const BG: Rgb = [24, 26, 33];
const FG: Rgb = [214, 219, 228];
const DIM: Rgb = [128, 136, 150];
const ACCENT: Rgb = [110, 196, 255];
const GOOD: Rgb = [120, 214, 140];
const BARC: Rgb = [44, 47, 58];

struct Canvas {
    w: usize,
    h: usize,
    px: Vec<u8>,
}

impl Canvas {
    fn new(w: usize, h: usize) -> Canvas {
        let mut c = Canvas { w, h, px: vec![0; w * h * 3] };
        c.rect(0, 0, w, h, BG);
        c
    }
    fn rect(&mut self, x: usize, y: usize, w: usize, h: usize, col: Rgb) {
        for yy in y..(y + h).min(self.h) {
            for xx in x..(x + w).min(self.w) {
                let i = (yy * self.w + xx) * 3;
                self.px[i..i + 3].copy_from_slice(&col);
            }
        }
    }
    fn circle(&mut self, cx: usize, cy: usize, r: usize, col: Rgb) {
        let r2 = (r * r) as i64;
        for yy in cy.saturating_sub(r)..=cy + r {
            for xx in cx.saturating_sub(r)..=cx + r {
                let dx = xx as i64 - cx as i64;
                let dy = yy as i64 - cy as i64;
                if dx * dx + dy * dy <= r2 && xx < self.w && yy < self.h {
                    let i = (yy * self.w + xx) * 3;
                    self.px[i..i + 3].copy_from_slice(&col);
                }
            }
        }
    }
    fn text(&mut self, x: usize, y: usize, s: &str, col: Rgb) {
        for (i, ch) in s.chars().enumerate() {
            let ch = match ch {
                '✓' => 'v',
                '✗' => 'x',
                c if (c as u32) < 128 => c,
                _ => '?',
            };
            if let Some(g) = BASIC_FONTS.get(ch) {
                for (row, bits) in g.iter().enumerate() {
                    for bit in 0..8 {
                        if bits & (1 << bit) != 0 {
                            self.rect(x + i * CELL_W + bit * SCALE, y + row * SCALE, SCALE, SCALE, col);
                        }
                    }
                }
            }
        }
    }
}

fn colour_for(line: &str) -> Rgb {
    let t = line.trim_start();
    if t.starts_with('$') {
        ACCENT
    } else if t.contains('✓') || t.starts_with("dev ") {
        GOOD
    } else if t.starts_with('#') || t.starts_with("  ") {
        DIM
    } else {
        FG
    }
}

/// The text shown in the picture: the banner, the main commands and the workspace commands.
fn screen_lines() -> Vec<String> {
    let mut v = vec![
        format!("$ dev --version"),
        format!("{} {}-{} ({})", VERSION, std::env::consts::OS, arch(), BUILD_DATE),
        String::new(),
        "$ dev --help".into(),
        "dev manages dev tools, environment variables and tasks, plus a workspace".into(),
        "for your projects.".into(),
        String::new(),
        "Usage: dev [OPTIONS] <COMMAND>".into(),
        String::new(),
        "Commands:".into(),
    ];
    for (c, d) in crate::COMMAND_SUMMARY {
        v.push(format!("  {:<12} {}", c, d));
    }
    v.push(String::new());
    v.push("$ dev workspace --help".into());
    for (c, d) in crate::WORKSPACE_SUMMARY {
        v.push(format!("  {:<12} {}", c, d));
    }
    v
}

pub fn arch() -> &'static str {
    match std::env::consts::ARCH {
        "x86_64" => "x64",
        "aarch64" => "arm64",
        a => a,
    }
}

pub fn take(path: &Path) -> Res<()> {
    let lines = screen_lines();
    let w = PAD * 2 + COLS * CELL_W;
    let h = BAR + PAD * 2 + lines.len() * CELL_H;
    let mut canvas = Canvas::new(w, h);
    // About 120 frames: the text is "typed" a little more each frame; the last frame is saved.
    let total: usize = lines.iter().map(|l| l.chars().count().max(1)).sum();
    let frames = 120;
    for f in 1..=frames {
        canvas.rect(0, 0, w, h, BG);
        canvas.rect(0, 0, w, BAR, BARC);
        canvas.circle(18, BAR / 2, 6, [237, 106, 94]);
        canvas.circle(38, BAR / 2, 6, [245, 191, 79]);
        canvas.circle(58, BAR / 2, 6, [98, 197, 84]);
        canvas.text(w / 2 - 6 * CELL_W, (BAR - 8 * SCALE) / 2, "dev - terminal", DIM);
        let budget = total * f / frames;
        let mut used = 0;
        for (i, l) in lines.iter().enumerate() {
            if used >= budget {
                break;
            }
            let n = l.chars().count().max(1);
            let shown: String = l.chars().take(budget - used).collect();
            canvas.text(PAD, BAR + PAD + i * CELL_H, &shown, colour_for(l));
            used += n;
        }
    }
    if let Some(p) = path.parent() {
        if !p.as_os_str().is_empty() {
            std::fs::create_dir_all(p).map_err(|e| e.to_string())?;
        }
    }
    let file = File::create(path).map_err(|e| format!("{}: {}", path.display(), e))?;
    let mut enc = png::Encoder::new(BufWriter::new(file), w as u32, h as u32);
    enc.set_color(png::ColorType::Rgb);
    enc.set_depth(png::BitDepth::Eight);
    let mut wr = enc.write_header().map_err(|e| e.to_string())?;
    wr.write_image_data(&canvas.px).map_err(|e| e.to_string())?;
    Ok(())
}
