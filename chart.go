package main

// 结果可视化（Q-05，F4/DFDS 的"动态显示"半边）：把当前结果网格的数据画成
// 柱状图/折线图——纯 fyne canvas 对象，无第三方依赖，与 erd.go 的绘制手法同源。
//
// 数据面：取结果网格的 TableData（与 showPin/showResult 的输入同源），X 列取
// 第一个非数值文本列（默认第 0 列），Y 列取第一个数值列；最多画 60 个类目，
// 超出截断并标注（图表是"看趋势/离群"，不是全量展示——DFDS 的原则）。

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// chartCategories 上限：超过则取前 N 并提示（避免把千行结果压成一屏噪声）。
const chartCategories = 60

// numericColumnIndex 找第一个数值列（isNumericType 复用网格的判定）。
func numericColumnIndex(data *TableData) int {
	for i, c := range data.Cols {
		if isNumericType(c.Type) {
			return i
		}
	}
	return -1
}

// chartPoints 把结果数据抽成 (label, value) 序列：X 取 labelCol，Y 取 valueCol；
// NULL/不可解析的值跳过；类目超上限截断。
func chartPoints(data *TableData, labelCol, valueCol int) ([]string, []float64, int) {
	labels := make([]string, 0, len(data.Rows))
	values := make([]float64, 0, len(data.Rows))
	dropped := 0
	for _, row := range data.Rows {
		if labelCol < 0 || labelCol >= len(row) || valueCol < 0 || valueCol >= len(row) {
			continue
		}
		cell := row[valueCol]
		if cell == nil {
			dropped++
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(*cell), 64)
		if err != nil {
			dropped++
			continue
		}
		lbl := "?"
		if row[labelCol] != nil {
			lbl = *row[labelCol]
		}
		labels = append(labels, lbl)
		values = append(values, v)
		if len(labels) >= chartCategories {
			break
		}
	}
	return labels, values, dropped
}

// fyneChart 是一张柱状/折线图：纯 canvas 对象的容器，尺寸随父容器自适应。
type fyneChart struct {
	widget.BaseWidget
	kind    string // "bar" | "line"
	labels  []string
	values  []float64
	bars    []*canvas.Rectangle
	polys   []*canvas.Line
	axis    []*canvas.Line
	ticks   []*canvas.Text
	xlabels []*canvas.Text
	title   *canvas.Text
	note    *canvas.Text
}

func newFyneChart(kind string, labels []string, values []float64, titleText, noteText string) *fyneChart {
	c := &fyneChart{kind: kind, labels: labels, values: values}
	c.ExtendBaseWidget(c)
	c.title = canvas.NewText(titleText, theme.Color(theme.ColorNameForeground))
	c.title.TextSize = float32(11)
	c.title.TextStyle = fyne.TextStyle{Bold: true}
	c.note = canvas.NewText(noteText, theme.Color(theme.ColorNamePlaceHolder))
	c.note.TextSize = float32(11)
	return c
}

// MinSize 给图表一个可用的默认画布（Split 里被拖动时按比例缩放）。
func (c *fyneChart) MinSize() fyne.Size {
	return fyne.NewSize(320, 200)
}

func (c *fyneChart) CreateRenderer() fyne.WidgetRenderer {
	c.rebuild(fyne.NewSize(640, 320))
	return &fyneChartRenderer{chart: c}
}

type fyneChartRenderer struct {
	chart *fyneChart
	objs  []fyne.CanvasObject
}

func (r *fyneChartRenderer) Layout(size fyne.Size) {
	r.chart.rebuild(size)
	r.objs = r.chart.allObjects()
}

func (r *fyneChartRenderer) Objects() []fyne.CanvasObject { return r.chart.allObjects() }
func (r *fyneChartRenderer) MinSize() fyne.Size           { return fyne.NewSize(320, 200) }
func (r *fyneChartRenderer) Refresh() { r.chart.rebuild(r.chart.Size()) }
func (r *fyneChartRenderer) Destroy() {}

// rebuild 按 size 重新生成/定位所有图形对象（Layout 每次拖动都调）。
func (c *fyneChart) rebuild(size fyne.Size) {
	fg := theme.Color(theme.ColorNameForeground)
	accent := theme.Color(theme.ColorNamePrimary)
	grid := theme.Color(theme.ColorNameSeparator)
	if grid == nil {
		grid = fg
	}
	// 边距：标题/注释占上 36px，X 轴标签占下 26px，左 48px 给 Y 刻度。
	left, bottom, top := float32(48), float32(26), float32(36)
	w := size.Width - left - 8
	h := size.Height - top - bottom
	if w < 40 || h < 30 || len(c.values) == 0 {
		return
	}
	// Y 轴范围：含 0（柱状图的基线是 0）。
	maxV := c.values[0]
	minV := c.values[0]
	for _, v := range c.values {
		if v > maxV {
			maxV = v
		}
		if v < minV {
			minV = v
		}
	}
	if c.kind == "bar" {
		minV = 0
	}
	if maxV == minV {
		maxV = minV + 1
	}
	span := maxV - minV
	yFor := func(v float32) float32 { return top + h*(1-(v-float32(minV))/float32(span)) }
	n := float32(len(c.values))
	// 网格线 + Y 刻度（4 条）。
	c.axis = c.axis[:0]
	c.ticks = c.ticks[:0]
	for i := 0; i <= 4; i++ {
		frac := float32(i) / 4
		y := top + h*frac
		ln := canvas.NewLine(grid)
		ln.Position1 = fyne.NewPos(left, y)
		ln.Position2 = fyne.NewPos(left+w, y)
		ln.StrokeWidth = 1
		c.axis = append(c.axis, ln)
		tick := canvas.NewText(trimFloat(float64(maxV-float64(span)*float64(frac))), fg)
		tick.TextSize = float32(11)
		tick.Alignment = fyne.TextAlignTrailing
		tick.Move(fyne.NewPos(2, y-7))
		c.ticks = append(c.ticks, tick)
	}
	c.bars = c.bars[:0]
	c.polys = c.polys[:0]
	slot := w / n
	bw := slot * 0.7
	if c.kind == "line" {
		// 折线：相邻两点连线（段数 = n-1）。
		for i := 0; i < len(c.values)-1; i++ {
			ln := canvas.NewLine(accent)
			ln.StrokeWidth = 2
			ln.Position1 = fyne.NewPos(left+slot*float32(i)+slot/2, yFor(float32(c.values[i])))
			ln.Position2 = fyne.NewPos(left+slot*float32(i+1)+slot/2, yFor(float32(c.values[i+1])))
			c.polys = append(c.polys, ln)
		}
	} else {
		for i, v := range c.values {
			bar := canvas.NewRectangle(accent)
			x := left + slot*float32(i) + (slot-bw)/2
			y := yFor(float32(v))
			bar.Move(fyne.NewPos(x, y))
			bar.Resize(fyne.NewSize(bw, top+h-y))
			c.bars = append(c.bars, bar)
		}
	}
	// X 轴标签：最多标 12 个（间隔取样），避免挤成一团。
	c.xlabels = c.xlabels[:0]
	step := 1
	if n > 12 {
		step = int(n/12) + 1
	}
	for i := 0; i < len(c.labels); i += step {
		lb := canvas.NewText(chartLabel(c.labels[i], 12), fg)
		lb.TextSize = float32(11)
		lb.Alignment = fyne.TextAlignCenter
		lb.Move(fyne.NewPos(left+slot*float32(i), top+h+4))
		c.xlabels = append(c.xlabels, lb)
	}
	c.title.Resize(fyne.NewSize(w, 16))
	c.title.Move(fyne.NewPos(left, 4))
	c.note.Resize(fyne.NewSize(w, 14))
	c.note.Move(fyne.NewPos(left, 20))
}

func (c *fyneChart) allObjects() []fyne.CanvasObject {
	objs := []fyne.CanvasObject{c.title, c.note}
	objs = append(objs, toObjects(c.axis)...)
	objs = append(objs, toObjects(c.ticks)...)
	objs = append(objs, toObjects(c.bars)...)
	objs = append(objs, toObjects(c.polys)...)
	objs = append(objs, toObjects(c.xlabels)...)
	return objs
}

func toObjects[T fyne.CanvasObject](xs []T) []fyne.CanvasObject {
	out := make([]fyne.CanvasObject, 0, len(xs))
	for _, x := range xs {
		out = append(out, x)
	}
	return out
}

func trimFloat(v float64) string {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if len(s) > 8 {
		s = strings.TrimRight(strings.TrimRight(s[:8], "0"), ".")
	}
	return s
}

func colNames(data *TableData) []string {
	names := make([]string, len(data.Cols))
	for i, c := range data.Cols {
		names[i] = c.Name
	}
	return names
}

func chartLabel(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// buildChartPane 组装结果页的图表面板：类型切换（柱/折线）+ 列选择 + 图本体。
// 返回一个可放进 resultSlot 的容器；数据来自当前结果网格的 TableData。
func buildChartPane(data *TableData) fyne.CanvasObject {
	if data == nil || len(data.Rows) == 0 {
		return container.NewCenter(widget.NewLabel("无结果可绘制"))
	}
	labelCol := 0
	valueCol := numericColumnIndex(data)
	if valueCol < 0 {
		return container.NewCenter(widget.NewLabel("结果没有数值列可绘制（图表需要一列数值）"))
	}
	kind := "bar"
	labels, values, dropped := chartPoints(data, labelCol, valueCol)
	if len(labels) == 0 {
		return container.NewCenter(widget.NewLabel("选定列没有可解析的数值"))
	}
	// 列选择器：X=文本列，Y=数值列。
	xSel := widget.NewSelect(colNames(data), nil)
	ySel := widget.NewSelect(colNames(data), nil)
	xSel.SetSelected(data.Cols[labelCol].Name)
	ySel.SetSelected(data.Cols[valueCol].Name)
	kindSel := widget.NewSelect([]string{"柱状图", "折线图"}, nil)
	kindSel.SetSelected("柱状图")
	titleText := fmt.Sprintf("%s × %s", data.Cols[labelCol].Name, data.Cols[valueCol].Name)
	var holder *fyneChart
	var chartSlot *fyne.Container
	remake := func() {
		labels, values, dropped = chartPoints(data, labelCol, valueCol)
		note := fmt.Sprintf("%d 类目", len(labels))
		if dropped > 0 {
			note += fmt.Sprintf("（%d 行 NULL/非数值已跳过）", dropped)
		}
		if int(labelCol) >= 0 && len(labels) >= chartCategories {
			note += fmt.Sprintf("（仅前 %d 行）", chartCategories)
		}
		next := newFyneChart(kind, labels, values, titleText, note)
		if holder != nil {
			// 替换 Stack 里的图本体。
			holder = next
		}
		chartSlot.Objects = []fyne.CanvasObject{next}
		chartSlot.Refresh()
	}
	chartSlot = container.NewStack()
	holder = newFyneChart(kind, labels, values, titleText, fmt.Sprintf("%d 类目", len(labels)))
	chartSlot.Objects = []fyne.CanvasObject{holder}
	xSel.OnChanged = func(s string) {
		for i, c := range data.Cols {
			if c.Name == s {
				labelCol = i
			}
		}
		remake()
	}
	ySel.OnChanged = func(s string) {
		for i, c := range data.Cols {
			if c.Name == s {
				valueCol = i
			}
		}
		remake()
	}
	kindSel.OnChanged = func(s string) {
		if s == "折线图" {
			kind = "line"
		} else {
			kind = "bar"
		}
		remake()
	}
	_ = sort.Strings // keep import if unused elsewhere
	controls := container.NewHBox(widget.NewLabel("类型"), kindSel, widget.NewLabel("X"), xSel, widget.NewLabel("Y"), ySel)
	return container.NewBorder(controls, nil, nil, nil, chartSlot)
}
