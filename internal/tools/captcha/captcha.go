// Package captcha exposes the pure-image slider solver as a read-only tool.
// It decodes two images (background + puzzle piece), locates the gap and
// optionally renders a human-like drag track. It never opens a browser and
// never sends a request: the caller submits the answer itself.
package captcha

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	slider "github.com/c1ayooo/xalgorix/v4/internal/captcha"
	"github.com/c1ayooo/xalgorix/v4/internal/sandbox"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

const maxImageBytes = 8 << 20

var imageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true}

// Register adds solve_slider_captcha to the registry.
func Register(r *tools.Registry) {
	r.Register(&tools.Tool{
		Name: "solve_slider_captcha",
		Description: "Solve an image-based slider captcha: pass the background image and the puzzle piece (file path " +
			"inside the scan workspace, a data URL, or bare base64) and receive the gap offset, the drag distance, " +
			"the y coordinate and a confidence score, optionally with a human-like drag track (x/y/ms points). " +
			"Pure image analysis — no browser, no requests. Stop after two failures and switch approach: repeated " +
			"wrong answers can lock the account.",
		Parameters: []tools.Parameter{
			{Name: "background", Description: "background image: workspace path, data URL or bare base64", Required: true},
			{Name: "piece", Description: "puzzle piece image: workspace path, data URL or bare base64", Required: true},
			{Name: "initial_x", Description: "visible left edge of the piece (optional; sets distance = offset - initial_x)", Required: false},
			{Name: "min_offset", Description: "search lower bound in pixels (optional)", Required: false},
			{Name: "max_offset", Description: "search upper bound in pixels (optional)", Required: false},
			{Name: "with_track", Description: "include a human-like drag track (default false)", Required: false},
			{Name: "seed", Description: "track RNG seed (optional; same seed reproduces the same track)", Required: false},
		},
		Execute: func(args map[string]string) (tools.Result, error) {
			return solve(r.GetScanContextID(), args)
		},
	})
}

func solve(contextID string, args map[string]string) (tools.Result, error) {
	sc := scanctx.Get(contextID)
	bg, bgSrc, err := imageBytes(sc, "background", args["background"])
	if err != nil {
		return tools.Result{Error: "background: " + err.Error()}, nil
	}
	piece, pieceSrc, err := imageBytes(sc, "piece", args["piece"])
	if err != nil {
		return tools.Result{Error: "piece: " + err.Error()}, nil
	}
	opts := slider.SolveOptions{
		InitialX:  atoiDefault(args["initial_x"], 0),
		MinOffset: atoiDefault(args["min_offset"], 0),
		MaxOffset: atoiDefault(args["max_offset"], 0),
	}
	res, err := slider.SolveSlider(bg, piece, opts)
	if err != nil {
		return tools.Result{Error: "solve failed: " + err.Error()}, nil
	}
	out := map[string]any{
		"background_source": bgSrc,
		"piece_source":      pieceSrc,
		"offset":            res.Offset,
		"distance":          res.Distance,
		"y":                 res.Y,
		"confidence":        res.Confidence,
		"score":             res.Score,
		"background_size":   []int{res.BackgroundWidth, res.BackgroundHeight},
		"piece_size":        []int{res.PieceWidth, res.PieceHeight},
		"elapsed_ms":        res.ElapsedMS,
		"note": "offset is the gap x in background coordinates; the drag distance usually subtracts the piece's " +
			"initial left edge (pass initial_x to get distance directly). Verify the response after submitting; " +
			"do not retry the same session repeatedly.",
	}
	if strings.EqualFold(strings.TrimSpace(args["with_track"]), "true") {
		out["track"] = slider.HumanTrack(res.Distance, slider.TrackOptions{Seed: atoi64Default(args["seed"], 0)})
	}
	payload, _ := json.MarshalIndent(out, "", "  ")
	return tools.Result{Output: string(payload)}, nil
}

func atoiDefault(raw string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return n
}

func atoi64Default(raw string, fallback int64) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

// imageBytes accepts a data URL, bare base64, or a workspace file path. Paths
// go through the shared read-deny policy; the workspace root is the scan dir so
// a relative path can never escape the session.
func imageBytes(sc *scanctx.ScanContext, field, raw string) ([]byte, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, "", fmt.Errorf("%s is required", field)
	}
	if len(raw) > maxImageBytes*2 {
		return nil, "", fmt.Errorf("input too large (%d bytes); per-image limit is %d", len(raw), maxImageBytes)
	}
	if strings.HasPrefix(strings.ToLower(raw), "data:") {
		data, err := slider.DecodeDataURL(raw)
		if err != nil {
			return nil, "", err
		}
		if len(data) > maxImageBytes {
			return nil, "", fmt.Errorf("decoded image is %d bytes, limit is %d", len(data), maxImageBytes)
		}
		return data, "data-url", nil
	}
	if imageExts[strings.ToLower(filepath.Ext(raw))] || looksLikePath(raw) {
		if sc == nil {
			return nil, "", fmt.Errorf("no active scan context: pass a data URL or base64 instead of a path")
		}
		path := raw
		if !filepath.IsAbs(path) {
			path = filepath.Join(sc.ScanDir, path)
		}
		canonical, err := sandbox.Default().CheckRead(sc, "solve_slider_captcha", path)
		if err != nil {
			return nil, "", err
		}
		info, err := os.Stat(canonical)
		if err != nil {
			return nil, "", err
		}
		if info.Size() > maxImageBytes {
			return nil, "", fmt.Errorf("image is %d bytes, limit is %d", info.Size(), maxImageBytes)
		}
		data, err := os.ReadFile(canonical)
		if err != nil {
			return nil, "", err
		}
		return data, canonical, nil
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, "", fmt.Errorf("not a data URL, base64 payload or image path")
	}
	if len(data) > maxImageBytes {
		return nil, "", fmt.Errorf("decoded image is %d bytes, limit is %d", len(data), maxImageBytes)
	}
	return data, "base64", nil
}

// looksLikePath avoids treating a long base64 blob (which may contain '/') as
// a path just because it has no image extension.
func looksLikePath(raw string) bool {
	if filepath.IsAbs(raw) {
		return true
	}
	if strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") {
		return true
	}
	if strings.ContainsAny(raw, "\\ \n") {
		return false
	}
	if len(raw) > 400 {
		return false
	}
	return strings.Contains(raw, "/") || strings.Contains(raw, ".")
}
