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
	// outcomeDegenerate — вход не конечное число (NaN, ±Inf): считать нечего,
	// результат бессмыслен.
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
//
// Итерация идёт не по самому x, а по его мантиссе: x = m·2^e, где e кратно 3
// и m ∈ [0.5, 4), тогда ∛x = ∛m·2^(e/3). Поэтому число шагов не зависит от
// порядка x, промежуточные значения не переполняются и не уходят в ноль, а
// масштабирование обратно через math.Ldexp точное. Раньше итерация стартовала
// с x/3 и останавливалась по абсолютной точности 1e-10: на |x| ≥ ~1e27 ей не
// хватало 100 шагов, на |x| ≤ ~1e-13 абсолютный критерий был слишком грубым,
// и в обоих случаях сервис отдавал неверный результат.
func cubeRoot(x float64) (float64, calcStats) {
	switch {
	case x == 0:
		return 0, calcStats{Outcome: outcomeTrivial}
	case math.IsNaN(x) || math.IsInf(x, 0):
		return x, calcStats{Outcome: outcomeDegenerate}
	case x < 0:
		z, stats := cubeRoot(-x)
		return -z, stats
	}

	const (
		// relTolerance — относительная точность остановки: несколько ulp
		// float64. Соседние float64 отличаются на ~2.2e-16, так что колебание
		// между ними тоже считается сходимостью.
		relTolerance = 1e-15
		maxIter      = 100
	)

	frac, exp := math.Frexp(x) // x = frac·2^exp, frac ∈ [0.5, 1)
	shift := (exp%3 + 3) % 3
	m := math.Ldexp(frac, shift) // m ∈ [0.5, 4)
	exp -= shift                 // теперь exp кратно 3

	// Линейное приближение ∛m на [0.5, 4) с ошибкой до ~7%: дальше метод
	// Ньютона сходится квадратично, за 3–5 шагов.
	z := 0.7441 + 0.2268*m
	for i := 1; i <= maxIter; i++ {
		next := (2*z + m/(z*z)) / 3
		if math.Abs(next-z) <= relTolerance*next {
			return math.Ldexp(next, exp/3), calcStats{Iterations: i, Outcome: outcomeConverged}
		}
		z = next
	}

	return math.Ldexp(z, exp/3), calcStats{Iterations: maxIter, Outcome: outcomeMaxIter}
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
