-- +goose Up
CREATE TABLE payments.cod_parcel_outcomes (
    attempt_id TEXT NOT NULL REFERENCES payments.payment_attempts (attempt_id),
    shipment_id TEXT NOT NULL,
    total_parcels INTEGER NOT NULL CHECK (total_parcels BETWEEN 1 AND 2),
    collected BOOLEAN NOT NULL,
    amount_vnd BIGINT NOT NULL CHECK (amount_vnd >= 0 AND (collected OR amount_vnd = 0)),
    PRIMARY KEY (attempt_id, shipment_id)
);

-- +goose Down
DROP TABLE payments.cod_parcel_outcomes;
