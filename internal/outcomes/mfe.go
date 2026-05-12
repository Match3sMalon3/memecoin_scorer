package outcomes

// OutcomeFromMFE classifies a price path by maximum favorable excursion.
// The returned multiple is max(price/path anchor), so 1.20 means +20%.
func OutcomeFromMFE(priceAtSignal float64, prices []float64) (outcome string, multiple float64, maxPrice float64) {
	if priceAtSignal <= 0 {
		return "unavailable", 0, 0
	}
	for _, price := range prices {
		if price > maxPrice {
			maxPrice = price
		}
	}
	if maxPrice <= 0 {
		return "unavailable", 0, 0
	}
	multiple = maxPrice / priceAtSignal
	if multiple >= 1.20 {
		return "hit", multiple, maxPrice
	}
	return "miss", multiple, maxPrice
}
