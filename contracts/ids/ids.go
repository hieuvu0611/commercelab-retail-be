// Package ids holds identifiers that more than one service must derive the same way.
package ids

// PaymentAttemptID is the id of the payment attempt of an order. An order has exactly one attempt (a failed
// payment closes the order; the customer places a new one), so the payment service creates the attempt under
// this id and the order service can show it without storing it.
func PaymentAttemptID(orderID string) string { return "PAY-" + orderID }
