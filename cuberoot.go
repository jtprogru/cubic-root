package main

import "math"

// outcome — чем закончилось вычисление кубического корня. Значения идут в
// label outcome метрики cubic_root_calculations_total.
type outcome string

const (
	// outcomeConverged — итерация сошлась с заданной точностью.
	outcomeConverged outcome = "converged"
	// outcomeMaxIter — итерация упёрлась в лимит шагов, результат не гарантирован.
	outcomeMaxIter outcome = "max_iter"
	// outcomeDegenerate — z обнулился или ушёл в Inf, итерация прервана.
	outcomeDegenerate outcome = "degenerate"
	// outcomeTrivial — d = 0, считать нечего.
	outcomeTrivial outcome = "trivial"
)

var outcomes = []outcome{outcomeConverged, outcomeMaxIter, outcomeDegenerate, outcomeTrivial}

// failed сообщает, что результату вычисления нельзя доверять.
func (o outcome) failed() bool {
	return o == outcomeMaxIter || o == outcomeDegenerate
}

// calcStats — сколько работы потребовало вычисление и чем оно закончилось.
type calcStats struct {
	// Iterations — число выполненных шагов метода Ньютона.
	Iterations int
	Outcome    outcome
}

// cubeRoot считает кубический корень из x методом Ньютона.
func cubeRoot(x float64) (float64, calcStats) {
	if x == 0 {
		return 0, calcStats{Outcome: outcomeTrivial}
	}

	const (
		precision = 1e-10
		maxIter   = 100
	)

	z := x / 3
	for i := 0; i < maxIter; i++ {
		// z может занулиться или уйти в Inf на экстремально малых/больших
		// входах — в этом случае x/(z*z) даёт Inf/NaN и итерация перестаёт
		// сходиться. Прерываемся, чтобы цикл всегда завершался.
		if z == 0 || math.IsInf(z, 0) {
			return z, calcStats{Iterations: i, Outcome: outcomeDegenerate}
		}
		nextZ := (2*z + x/(z*z)) / 3
		if math.Abs(nextZ-z) < precision {
			return z, calcStats{Iterations: i + 1, Outcome: outcomeConverged}
		}
		z = nextZ
	}

	return z, calcStats{Iterations: maxIter, Outcome: outcomeMaxIter}
}

// relativeError возвращает |z³−d|/|d|, посчитанную как |z²/d·z − 1|: так
// промежуточные значения не переполняются и не уходят в ноль на краях
// диапазона float64. Ошибка от 100% и выше, а также NaN возвращаются как 1:
// такой ответ уже бесполезен, а +Inf в наблюдении навсегда сломал бы _sum
// гистограммы.
func relativeError(d, z float64) float64 {
	e := math.Abs(z*z/d*z - 1)
	if math.IsNaN(e) || e > 1 {
		return 1
	}
	return e
}
