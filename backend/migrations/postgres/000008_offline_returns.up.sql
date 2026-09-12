-- A return taken at the counter while the connection was down.
--
-- Raising a return is a claim: the goods are back on the counter and the
-- customer has been paid, but nothing moves in the books until somebody
-- approves it. That makes a return safe to take offline in a way approving one
-- would not be — approval puts stock back and has to be decided against what the
-- shelf and the invoice actually say.
--
-- What cannot wait is the claim itself. The cashier has handed money over. A
-- return the server refuses on sync is a shop that has paid out against a record
-- it does not have, so the same rule applies as to a sale: it lands, and what
-- could not be verified becomes an exception for somebody.
--
-- The two things that can be wrong are both about the invoice behind it. Either
-- the invoice never sold that product, or more is coming back than went out —
-- and the till can only check either against invoices it happens to hold.

ALTER TYPE sync_exception_kind ADD VALUE IF NOT EXISTS 'over_returned';
