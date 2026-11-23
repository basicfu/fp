package captcha

// Tolerance 是拼块落点与缺口的最大误差(逻辑像素,600 宽坐标系)。
const Tolerance = 6

const (
	minTrackPoints = 8
	maxTrackPoints = 200
	minTrackMs     = 300
	maxTrackMs     = 15000
)

// CheckTrack 只挡"连轨迹都不伪造"的脚本:点数、耗时、终点、非匀速、y 有抖动。
// 认真伪造的轨迹挡不住,兜底是 fp 的限频(见规格)。track 每点 [dx, dy, t毫秒],从按下起算。
func CheckTrack(track [][3]int, x int) bool {
	n := len(track)
	if n < minTrackPoints || n > maxTrackPoints {
		return false
	}
	dur := track[n-1][2] - track[0][2]
	if dur < minTrackMs || dur > maxTrackMs {
		return false
	}
	if d := track[n-1][0] - x; d > Tolerance || d < -Tolerance {
		return false
	}
	yMoved := false
	minV, maxV := 1e9, -1e9
	for i := 1; i < n; i++ {
		dt := track[i][2] - track[i-1][2]
		if dt < 0 {
			return false
		}
		if track[i][1] != track[0][1] {
			yMoved = true
		}
		if dt == 0 {
			continue
		}
		v := float64(track[i][0]-track[i-1][0]) / float64(dt)
		minV, maxV = min(minV, v), max(maxV, v)
	}
	return yMoved && maxV-minV > 0.05
}
