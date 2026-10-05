-- Where each admin's card of a booking went, to redraw it when the booking changes.
CREATE TABLE cards (
    booking_id INTEGER NOT NULL REFERENCES bookings(id),
    chat_id    INTEGER NOT NULL,
    message_id INTEGER NOT NULL
);

CREATE INDEX cards_booking ON cards(booking_id);
