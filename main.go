package main

import (
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
)

func cubicRootHandler(w http.ResponseWriter, r *http.Request) {
	// Получаем значение параметра "d" из запроса
	dParam := r.URL.Query().Get("d")
	if dParam == "" {
		http.Error(w, "Missing parameter 'd'", http.StatusBadRequest)
		return
	}

	// Конвертируем строку в число с плавающей запятой
	d, err := strconv.ParseFloat(dParam, 64)
	if err != nil {
		http.Error(w, "Invalid parameter 'd'", http.StatusBadRequest)
		return
	}

	// Вычисляем кубический корень
	result := math.Cbrt(d)

	// Округляем результат до 6 знаков после запятой
	result = math.Round(result*1e6) / 1e6

	// Формируем ответ
	fmt.Fprintf(w, "%.6f\n", result)
}

func main() {
	http.HandleFunc("/sqrt", cubicRootHandler)

	// Запуск сервера на порту 8080
	fmt.Println("Server is running on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
