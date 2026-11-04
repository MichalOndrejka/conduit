"""Generates internal/eval/datasets/synthetic.json:

    python internal/eval/datasets/gen_synthetic.py internal/eval/datasets/synthetic.json

Three fictional companies whose engineering data is organized differently,
each with five topics. Every topic has one document per intent (requirement,
test case, work item, code, test code, documentation), so documents about
the same topic but of a different kind compete in search: exactly what a
merged collection must keep apart.
"""
import json
import sys

def wi(id, intent, title, wtype, state, body, project, area=None, tags=None, number=None):
    lines = [title, f"Type: {wtype}", f"State: {state}"]
    if area:
        lines.append(f"Area Path: {area}")
    if tags:
        lines.append("Tags: " + "; ".join(tags))
    text = "\n".join(lines) + "\n\n" + body.strip()
    fields = {"project": [project], "work_item_type": [wtype], "state": [state]}
    if tags:
        fields["tags"] = tags
    return {"id": id, "intent": intent, "shape": "work_item", "title": title, "text": text, "fields": fields}

def issue(id, intent, number, title, itype, state, labels, body, owner, repo):
    lines = [title, f"Issue: #{number}"]
    if itype:
        lines.append(f"Type: {itype}")
    lines += [f"State: {state}", "Labels: " + ", ".join(labels)]
    text = "\n".join(lines) + "\n\n" + body.strip()
    fields = {"project": [owner], "repository": [repo], "work_item_type": [itype or "Issue"], "state": [state], "tags": labels}
    return {"id": id, "intent": intent, "shape": "work_item", "title": title, "text": text, "fields": fields}

def file(id, intent, path, content, project, repo):
    ext = path.rsplit(".", 1)[-1] if "." in path.rsplit("/", 1)[-1] else ""
    fields = {"project": [project], "repository": [repo], "path": [path]}
    if ext:
        fields["extension"] = [ext]
    return {"id": id, "intent": intent, "shape": "file", "title": path, "text": path + "\n\n" + content.strip(), "fields": fields}

def q(id, intent, text, relevant, note=None):
    d = {"id": id, "intent": intent, "text": text, "relevant": relevant}
    if note:
        d["note"] = note
    return d

# ── Northwind: Azure DevOps, custom process ───────────────────────────────────
NW, NWR = "Northwind", "Storefront"
nw_docs = [
    # Topic: password reset
    wi("nw-req-reset", "requirement", "Password reset via emailed link", "Product Requirement", "Active", """
The system shall let a registered customer reset a forgotten password by requesting a reset link sent to their verified email address.
Acceptance criteria:
- The reset link is single-use and expires 30 minutes after it is issued.
- Requesting a new link invalidates any earlier unused link.
- The response to a reset request is identical whether or not the email exists, so accounts cannot be enumerated.
- After a successful reset all active sessions of the customer are signed out.
""", NW, area="Northwind\\Identity", tags=["security", "gdpr"]),
    wi("nw-tc-reset", "test_case", "Verify reset link expires after 30 minutes", "Test Case", "Ready", """
Steps:
1. Request a password reset for customer test.user@northwind.test.
2. Open the received email and wait 31 minutes.
3. Open the reset link.
Expected: the page shows "This link has expired" and offers to send a new link. The old password still works.
""", NW, area="Northwind\\Identity"),
    wi("nw-bug-reset", "work_item", "Reset email sent twice when the submit button is double-clicked", "Bug", "Active", """
Repro: on the Forgot password page, double-click Send link. Two emails arrive, and the first link no longer works because the second request invalidated it.
Customers report the link "never works". Disable the button after the first click and make the endpoint idempotent for 10 seconds per email.
""", NW, area="Northwind\\Identity", tags=["customer-reported"]),
    file("nw-code-reset", "code", "src/Storefront/Identity/PasswordResetService.cs", """
public sealed class PasswordResetService
{
    private static readonly TimeSpan LinkLifetime = TimeSpan.FromMinutes(30);

    public async Task RequestResetAsync(string email, CancellationToken ct)
    {
        var customer = await _customers.FindByEmailAsync(email, ct);
        if (customer is null) return; // same response either way: no account enumeration
        await _tokens.RevokeAllAsync(customer.Id, TokenPurpose.PasswordReset, ct);
        var token = await _tokens.IssueAsync(customer.Id, TokenPurpose.PasswordReset, LinkLifetime, ct);
        await _mailer.SendResetLinkAsync(customer.Email, token, ct);
    }

    public async Task<ResetResult> CompleteResetAsync(string token, string newPassword, CancellationToken ct)
    {
        var claim = await _tokens.RedeemAsync(token, TokenPurpose.PasswordReset, ct);
        if (claim is null) return ResetResult.Expired;
        await _customers.SetPasswordAsync(claim.CustomerId, newPassword, ct);
        await _sessions.SignOutEverywhereAsync(claim.CustomerId, ct);
        return ResetResult.Success;
    }
}
""", NW, NWR),
    file("nw-test-reset", "test_code", "tests/Storefront.Tests/Identity/PasswordResetServiceTests.cs", """
public class PasswordResetServiceTests
{
    [Fact]
    public async Task CompleteReset_WithExpiredToken_ReturnsExpired()
    {
        var clock = new FakeClock();
        var service = CreateService(clock);
        var token = await service.IssueTestTokenAsync("ann@northwind.test");
        clock.Advance(TimeSpan.FromMinutes(31));
        var result = await service.CompleteResetAsync(token, "N3w-password!", default);
        Assert.Equal(ResetResult.Expired, result);
    }

    [Fact]
    public async Task RequestReset_RevokesEarlierTokens()
    {
        var service = CreateService();
        var first = await service.IssueTestTokenAsync("ann@northwind.test");
        await service.RequestResetAsync("ann@northwind.test", default);
        Assert.Equal(ResetResult.Expired, await service.CompleteResetAsync(first, "x", default));
    }
}
""", NW, NWR),
    file("nw-doc-reset", "documentation", "wiki/Identity/Password-reset-flow.md", """
# Password reset flow

Customers who forget their password request a reset link from the sign-in page. The Identity service issues a single-use token, stores only its hash, and emails a link that is valid for 30 minutes.

Support agents cannot see or resend reset links. If a customer says the link does not work, ask them to request a new one and to open only the most recent email, because every new request revokes older links.
""", NW, NWR),

    # Topic: order discounts
    wi("nw-req-discount", "requirement", "Tiered order discounts", "Product Requirement", "Approved", """
The checkout shall apply a discount based on the order subtotal before tax:
- 5% for subtotals from 100 to 249.99 EUR
- 10% for subtotals from 250 EUR
Discounts never stack with promotional codes; the larger of the two applies. The discount line is shown on the order confirmation and the invoice.
""", NW, area="Northwind\\Checkout"),
    wi("nw-tc-discount", "test_case", "Verify the larger of tier discount and promo code applies", "Test Case", "Ready", """
Steps:
1. Add items totalling 300 EUR to the basket (10% tier discount = 30 EUR).
2. Apply promo code SPRING20 (20 EUR off).
Expected: only the 30 EUR tier discount is applied and the promo code is shown as "not combinable".
""", NW, area="Northwind\\Checkout"),
    wi("nw-bug-discount", "work_item", "Tier discount calculated on subtotal including tax", "Bug", "Resolved", """
Orders of 240 EUR net (285.60 EUR gross) receive the 10% tier discount although the net subtotal is below 250 EUR. The calculator uses the gross amount. Fixed in DiscountCalculator by using the net subtotal.
""", NW, area="Northwind\\Checkout"),
    file("nw-code-discount", "code", "src/Storefront/Checkout/DiscountCalculator.cs", """
public static class DiscountCalculator
{
    public static decimal TierDiscount(decimal netSubtotal) => netSubtotal switch
    {
        >= 250m => netSubtotal * 0.10m,
        >= 100m => netSubtotal * 0.05m,
        _ => 0m,
    };

    public static decimal Apply(Basket basket, PromoCode? promo)
    {
        var tier = TierDiscount(basket.NetSubtotal);
        var promoAmount = promo?.AmountFor(basket) ?? 0m;
        return Math.Max(tier, promoAmount); // discounts never stack
    }
}
""", NW, NWR),
    file("nw-test-discount", "test_code", "tests/Storefront.Tests/Checkout/DiscountCalculatorTests.cs", """
public class DiscountCalculatorTests
{
    [Theory]
    [InlineData(99.99, 0)]
    [InlineData(100, 5)]
    [InlineData(250, 25)]
    public void TierDiscount_UsesNetSubtotalBands(decimal net, decimal expected)
        => Assert.Equal(expected, DiscountCalculator.TierDiscount(net));

    [Fact]
    public void Apply_PicksLargerOfTierAndPromo()
    {
        var basket = Basket.WithNetSubtotal(300m);
        Assert.Equal(30m, DiscountCalculator.Apply(basket, PromoCode.Fixed(20m)));
    }
}
""", NW, NWR),
    file("nw-doc-discount", "documentation", "wiki/Checkout/Discounts-and-promotions.md", """
# Discounts and promotions

Northwind offers automatic tier discounts on larger orders and marketing promo codes. Only one of them applies per order: the checkout always picks the one worth more to the customer.

Marketing creates promo codes in the back office under Promotions. Tier thresholds are configured per market by the finance team.
""", NW, NWR),

    # Topic: invoice PDF export
    wi("nw-req-invoice", "requirement", "Downloadable PDF invoices", "Product Requirement", "Active", """
Customers shall be able to download a PDF invoice for every shipped order from their order history.
The invoice shows the legal entity, VAT numbers, each line with net price, VAT rate and amount, applied discounts and the payment method. Invoices are immutable once issued; corrections are made with a credit note.
""", NW, area="Northwind\\Billing", tags=["legal"]),
    wi("nw-tc-invoice", "test_case", "Verify invoice PDF lists VAT per rate", "Test Case", "Ready", """
Steps:
1. Place an order with one 19% VAT item and one 7% VAT item and ship it.
2. Download the invoice from Order history.
Expected: the PDF shows a VAT summary with one line per rate and the totals match the order confirmation.
""", NW, area="Northwind\\Billing"),
    wi("nw-story-invoice", "work_item", "As a business customer I want our VAT number printed on invoices", "User Story", "New", """
Business customers need their own VAT identification number on the invoice to reclaim VAT. Add an optional company VAT number to the account profile and print it under the billing address.
""", NW, area="Northwind\\Billing"),
    file("nw-code-invoice", "code", "src/Storefront/Billing/InvoicePdfRenderer.cs", """
public sealed class InvoicePdfRenderer
{
    public byte[] Render(Invoice invoice)
    {
        using var doc = new PdfDocument();
        var page = doc.AddPage();
        WriteHeader(page, invoice.LegalEntity, invoice.CustomerVatNumber);
        foreach (var line in invoice.Lines)
            WriteLine(page, line.Description, line.NetPrice, line.VatRate, line.VatAmount);
        WriteVatSummary(page, invoice.Lines.GroupBy(l => l.VatRate));
        WriteTotals(page, invoice);
        return doc.Save();
    }
}
""", NW, NWR),
    file("nw-test-invoice", "test_code", "tests/Storefront.Tests/Billing/InvoicePdfRendererTests.cs", """
public class InvoicePdfRendererTests
{
    [Fact]
    public void Render_GroupsVatSummaryByRate()
    {
        var invoice = InvoiceBuilder.With(line: 100m, vat: 0.19m).And(line: 50m, vat: 0.07m).Build();
        var text = PdfText.Extract(new InvoicePdfRenderer().Render(invoice));
        Assert.Contains("VAT 19%: 19.00", text);
        Assert.Contains("VAT 7%: 3.50", text);
    }
}
""", NW, NWR),
    file("nw-doc-invoice", "documentation", "wiki/Billing/Invoices-and-credit-notes.md", """
# Invoices and credit notes

An invoice is issued when an order ships and can be downloaded as PDF from the customer's order history. Issued invoices are never edited. To correct an amount, finance issues a credit note that references the original invoice number.

Invoice numbers are sequential per legal entity, as required by German tax law.
""", NW, NWR),

    # Topic: inventory sync
    wi("nw-req-inventory", "requirement", "Near real-time stock levels from the warehouse", "Product Requirement", "Active", """
Product pages shall show stock levels that are at most 5 minutes behind the warehouse management system. When a product's stock reaches zero it shall be shown as out of stock and cannot be added to the basket.
""", NW, area="Northwind\\Catalog"),
    wi("nw-tc-inventory", "test_case", "Verify product becomes unavailable when warehouse stock hits zero", "Test Case", "Design", """
Steps:
1. In the warehouse test system set the stock of SKU NW-1001 to 0.
2. Wait for the next inventory sync (max 5 minutes).
3. Open the product page.
Expected: "Out of stock" is shown and Add to basket is disabled.
""", NW, area="Northwind\\Catalog"),
    wi("nw-bug-inventory", "work_item", "Inventory sync job stops after warehouse API timeout", "Bug", "Active", """
When the warehouse API times out once, the sync job logs the exception and never schedules the next run. Stock levels freeze until the service restarts. Add retry with backoff and always reschedule.
""", NW, area="Northwind\\Catalog", tags=["incident"]),
    file("nw-code-inventory", "code", "src/Storefront/Catalog/InventorySyncJob.cs", """
public sealed class InventorySyncJob : BackgroundService
{
    private static readonly TimeSpan Interval = TimeSpan.FromMinutes(5);

    protected override async Task ExecuteAsync(CancellationToken ct)
    {
        while (!ct.IsCancellationRequested)
        {
            try
            {
                var levels = await _warehouse.GetStockLevelsAsync(ct);
                await _catalog.UpdateStockAsync(levels, ct);
            }
            catch (WarehouseTimeoutException ex)
            {
                _log.LogWarning(ex, "Warehouse timed out; retrying next interval");
            }
            await Task.Delay(Interval, ct);
        }
    }
}
""", NW, NWR),
    file("nw-test-inventory", "test_code", "tests/Storefront.Tests/Catalog/InventorySyncJobTests.cs", """
public class InventorySyncJobTests
{
    [Fact]
    public async Task Execute_AfterWarehouseTimeout_KeepsSyncing()
    {
        var warehouse = new FakeWarehouse().TimeoutOnce().ThenReturn(("NW-1001", 0));
        var catalog = new FakeCatalog();
        await RunJobForTwoIntervals(warehouse, catalog);
        Assert.Equal(0, catalog.StockOf("NW-1001"));
    }
}
""", NW, NWR),
    file("nw-doc-inventory", "documentation", "wiki/Catalog/Inventory-synchronisation.md", """
# Inventory synchronisation

Stock levels come from the warehouse management system (WMS). A background job in the Storefront pulls all levels every 5 minutes. If the WMS is down, the storefront keeps showing the last known levels; on-call is paged when no successful sync happened for 30 minutes.
""", NW, NWR),

    # Topic: loyalty points
    wi("nw-req-loyalty", "requirement", "Loyalty points for purchases", "Product Requirement", "Proposed", """
Members of the loyalty programme shall earn 1 point per full euro of net order value once the order is delivered. Points expire 24 months after they were earned. Points are deducted again when an order is returned.
""", NW, area="Northwind\\Loyalty"),
    wi("nw-tc-loyalty", "test_case", "Verify points are deducted after a return", "Test Case", "Ready", """
Steps:
1. As a loyalty member, buy an item for 80 EUR net and mark the order delivered (80 points).
2. Return the item and complete the refund.
Expected: the points balance decreases by 80 and the history shows "Return of order".
""", NW, area="Northwind\\Loyalty"),
    wi("nw-task-loyalty", "work_item", "Nightly job to expire loyalty points older than 24 months", "Task", "New", """
Create a scheduled job that removes points earned more than 24 months ago and writes an expiry entry into the points history. Run nightly at 02:00 CET.
""", NW, area="Northwind\\Loyalty"),
    file("nw-code-loyalty", "code", "src/Storefront/Loyalty/PointsLedger.cs", """
public sealed class PointsLedger
{
    public int PointsFor(Order order) => (int)Math.Floor(order.NetTotal);

    public void OnDelivered(Order order) => _entries.Add(LedgerEntry.Earned(order.CustomerId, PointsFor(order), order.Id));

    public void OnReturned(Order order) => _entries.Add(LedgerEntry.Reversed(order.CustomerId, -PointsFor(order), order.Id));

    public int Balance(Guid customerId, DateTimeOffset now) =>
        _entries.Where(e => e.CustomerId == customerId && e.EarnedAt > now.AddMonths(-24)).Sum(e => e.Points);
}
""", NW, NWR),
    file("nw-test-loyalty", "test_code", "tests/Storefront.Tests/Loyalty/PointsLedgerTests.cs", """
public class PointsLedgerTests
{
    [Fact]
    public void Balance_ExcludesPointsOlderThan24Months()
    {
        var ledger = new PointsLedger();
        ledger.OnDelivered(Order.Delivered(net: 80m, at: Now.AddMonths(-25)));
        ledger.OnDelivered(Order.Delivered(net: 20m, at: Now));
        Assert.Equal(20, ledger.Balance(Customer, Now));
    }
}
""", NW, NWR),
    file("nw-doc-loyalty", "documentation", "wiki/Loyalty/Loyalty-programme-overview.md", """
# Loyalty programme overview

The loyalty programme rewards repeat customers with points they can redeem for vouchers. Membership is free and opt-in from the account page. Customer service can view a member's points history but cannot add points manually; goodwill gestures are vouchers instead.
""", NW, NWR),
]

nw_queries = [
    q("nw-q1", "requirement", "How long must a password reset link stay valid according to the requirements?", ["nw-req-reset"]),
    q("nw-q2", "test_code", "Which unit test checks that an expired reset token is rejected?", ["nw-test-reset"]),
    q("nw-q3", "work_item", "Are there open bugs about customers receiving two reset emails?", ["nw-bug-reset"]),
    q("nw-q4", "test_case", "Manual test steps for combining a promo code with the tier discount", ["nw-tc-discount"]),
    q("nw-q5", "code", "Where is the tier discount for orders calculated?", ["nw-code-discount"]),
    q("nw-q6", "documentation", "Explain how discounts and promo codes work for the support team", ["nw-doc-discount"]),
    q("nw-q7", "requirement", "What has to be printed on a customer invoice?", ["nw-req-invoice"]),
    q("nw-q8", "documentation", "How do we correct an invoice that was already issued?", ["nw-doc-invoice"]),
    q("nw-q9", "test_code", "Tests for the VAT summary in the invoice PDF", ["nw-test-invoice"]),
    q("nw-q10", "test_case", "How do testers verify that a product shows out of stock?", ["nw-tc-inventory"]),
    q("nw-q11", "work_item", "Inventory stock levels freeze after a warehouse timeout - is this tracked?", ["nw-bug-inventory"]),
    q("nw-q12", "code", "Code of the background job that pulls stock levels from the warehouse", ["nw-code-inventory"]),
    q("nw-q13", "requirement", "When do loyalty points expire?", ["nw-req-loyalty"]),
    q("nw-q14", "test_case", "Test case for loyalty points after an order is returned", ["nw-tc-loyalty"]),
    q("nw-q15", "test_code", "Unit tests covering expiry of loyalty points", ["nw-test-loyalty"]),
]

# ── Contoso: Azure DevOps Scrum, requirements and test cases as Markdown ──────
CT, CTR = "Contoso", "payments-api"
ct_docs = [
    # Topic: refunds
    file("ct-req-refund", "requirement", "docs/requirements/REQ-014-refunds.md", """
# REQ-014 Refunds

## Requirement
Merchants can refund a captured payment fully or partially, as long as the sum of all refunds never exceeds the captured amount.

## Acceptance criteria
- A refund is only possible within 180 days of capture.
- Partial refunds may be issued multiple times until the captured amount is exhausted.
- Each refund produces a webhook event refund.succeeded or refund.failed.
""", CT, CTR),
    file("ct-tc-refund", "test_case", "docs/test-cases/TC-031-partial-refunds.md", """
# TC-031 Partial refunds up to the captured amount

Preconditions: a payment of 100.00 EUR has been captured.

| Step | Action | Expected result |
|---|---|---|
| 1 | Refund 60.00 EUR | Refund succeeds, refundable balance 40.00 EUR |
| 2 | Refund 50.00 EUR | Rejected with error amount_exceeds_refundable |
| 3 | Refund 40.00 EUR | Refund succeeds, refundable balance 0.00 EUR |
""", CT, CTR),
    wi("ct-pbi-refund", "work_item", "Allow refunds older than 180 days for enterprise merchants", "Product Backlog Item", "Committed", """
Enterprise merchants with an extended refund agreement need refunds up to 365 days after capture. Make the refund window a per-merchant setting with a default of 180 days.
""", CT, area="Contoso\\Payments"),
    file("ct-code-refund", "code", "internal/refunds/service.go", """
package refunds

// Refund issues a full or partial refund of a captured payment.
func (s *Service) Refund(ctx context.Context, paymentID string, amount Money) (*Refund, error) {
	p, err := s.payments.Get(ctx, paymentID)
	if err != nil {
		return nil, err
	}
	if time.Since(p.CapturedAt) > s.window(p.MerchantID) {
		return nil, ErrRefundWindowClosed
	}
	if amount.Cmp(p.Refundable()) > 0 {
		return nil, ErrAmountExceedsRefundable
	}
	r, err := s.acquirer.Refund(ctx, p, amount)
	s.events.Publish(ctx, eventFor(r, err))
	return r, err
}
""", CT, CTR),
    file("ct-test-refund", "test_code", "internal/refunds/service_test.go", """
package refunds

func TestRefundRejectsAmountAboveRefundableBalance(t *testing.T) {
	s := newTestService(t, capturedPayment("p1", eur(100)))
	if _, err := s.Refund(ctx, "p1", eur(60)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refund(ctx, "p1", eur(50)); !errors.Is(err, ErrAmountExceedsRefundable) {
		t.Fatalf("err = %v, want ErrAmountExceedsRefundable", err)
	}
}

func TestRefundAfterWindowIsRejected(t *testing.T) {
	s := newTestService(t, capturedPaymentAt("p1", eur(100), daysAgo(181)))
	if _, err := s.Refund(ctx, "p1", eur(10)); !errors.Is(err, ErrRefundWindowClosed) {
		t.Fatalf("err = %v", err)
	}
}
""", CT, CTR),
    file("ct-doc-refund", "documentation", "docs/architecture/refunds.md", """
# Refunds architecture

The refunds package validates a refund request against the payment's refundable balance and the merchant's refund window, then forwards it to the acquirer. Acquirer calls are synchronous; the result is published as a webhook event. Refund records are append-only and reconciled nightly against acquirer settlement files.
""", CT, CTR),

    # Topic: currency conversion
    file("ct-req-fx", "requirement", "docs/requirements/REQ-022-currency-conversion.md", """
# REQ-022 Currency conversion

## Requirement
Payments in a currency other than the merchant's settlement currency are converted at the rate locked when the payment is authorised.

## Acceptance criteria
- The locked rate and its source are stored with the payment.
- Rates older than 60 seconds are never used for a new authorisation.
- The converted amount is rounded half-even to the settlement currency's minor unit.
""", CT, CTR),
    file("ct-tc-fx", "test_case", "docs/test-cases/TC-044-stale-fx-rate.md", """
# TC-044 Authorisation with a stale exchange rate

Preconditions: the FX rate feed is paused so the latest USD/EUR rate is 90 seconds old.

| Step | Action | Expected result |
|---|---|---|
| 1 | Authorise a 50.00 USD payment for an EUR merchant | Authorisation fails with fx_rate_unavailable |
| 2 | Resume the rate feed and retry | Authorisation succeeds and stores the new rate |
""", CT, CTR),
    wi("ct-bug-fx", "work_item", "Rounding difference of one cent on JPY to EUR conversions", "Bug", "Active", """
Converted amounts from JPY are off by 0.01 EUR compared to the acquirer's settlement. We round half-up but the requirement says half-even. Affects reconciliation reports.
""", CT, area="Contoso\\Payments", tags=["reconciliation"]),
    file("ct-code-fx", "code", "internal/fx/converter.go", """
package fx

const maxRateAge = 60 * time.Second

// Convert converts amount to the target currency at the latest rate,
// rounding half-even to the target's minor unit.
func (c *Converter) Convert(ctx context.Context, amount Money, to Currency) (Money, Rate, error) {
	rate, err := c.rates.Latest(ctx, amount.Currency, to)
	if err != nil {
		return Money{}, Rate{}, err
	}
	if c.clock.Since(rate.FetchedAt) > maxRateAge {
		return Money{}, Rate{}, ErrRateUnavailable
	}
	return amount.Mul(rate.Value).RoundHalfEven(to.MinorUnits()), rate, nil
}
""", CT, CTR),
    file("ct-test-fx", "test_code", "internal/fx/converter_test.go", """
package fx

func TestConvertRejectsRateOlderThan60Seconds(t *testing.T) {
	c := newTestConverter(rateFetched(usd, eur, 0.92, 61*time.Second))
	if _, _, err := c.Convert(ctx, usd.Amount(5000), eur); !errors.Is(err, ErrRateUnavailable) {
		t.Fatalf("err = %v, want ErrRateUnavailable", err)
	}
}

func TestConvertRoundsHalfEven(t *testing.T) {
	c := newTestConverter(rateFetched(jpy, eur, 0.0061, 0))
	got, _, _ := c.Convert(ctx, jpy.Amount(1250), eur)
	if got != eur.Amount(762) {
		t.Fatalf("got %v", got)
	}
}
""", CT, CTR),
    file("ct-doc-fx", "documentation", "docs/runbooks/fx-rate-feed.md", """
# Runbook: FX rate feed outage

Symptoms: authorisations in foreign currencies fail with fx_rate_unavailable; the dashboard "FX rate age" is above 60 seconds.

1. Check the rate provider status page.
2. If the provider is down for more than 10 minutes, switch to the secondary provider with the feature flag fx.secondary-provider.
3. Post in #payments-incidents.
""", CT, CTR),

    # Topic: webhook retries
    file("ct-req-webhook", "requirement", "docs/requirements/REQ-030-webhook-delivery.md", """
# REQ-030 Webhook delivery

## Requirement
Every payment and refund event is delivered to the merchant's webhook endpoint at least once.

## Acceptance criteria
- Failed deliveries are retried with exponential backoff for up to 72 hours.
- Each delivery is signed with HMAC-SHA256 using the merchant's webhook secret.
- Merchants can see delivery attempts and replay an event from the dashboard.
""", CT, CTR),
    file("ct-tc-webhook", "test_case", "docs/test-cases/TC-052-webhook-retry-backoff.md", """
# TC-052 Webhook retries after endpoint failures

Preconditions: the merchant's test endpoint returns HTTP 500.

| Step | Action | Expected result |
|---|---|---|
| 1 | Capture a payment | First delivery attempt fails |
| 2 | Observe delivery attempts for one hour | Retries at roughly 1, 2, 4, 8, 16 and 32 minutes |
| 3 | Make the endpoint return 200 | Next retry succeeds and no further attempts are made |
""", CT, CTR),
    wi("ct-bug-webhook", "work_item", "Webhook retries stop after a service restart", "Bug", "Committed", """
Pending webhook retries are kept in memory, so a deploy drops all scheduled retries. Merchants miss events. Persist the retry schedule in the outbox table.
""", CT, area="Contoso\\Platform", tags=["incident"]),
    file("ct-code-webhook", "code", "internal/webhooks/dispatcher.go", """
package webhooks

// nextAttempt returns when a failed delivery is retried: exponential backoff
// starting at one minute, giving up 72 hours after the event.
func nextAttempt(event Event, attempt int, now time.Time) (time.Time, bool) {
	delay := time.Minute << min(attempt, 12)
	next := now.Add(delay)
	if next.Sub(event.CreatedAt) > 72*time.Hour {
		return time.Time{}, false
	}
	return next, true
}

func sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
""", CT, CTR),
    file("ct-test-webhook", "test_code", "internal/webhooks/dispatcher_test.go", """
package webhooks

func TestNextAttemptBacksOffExponentially(t *testing.T) {
	ev := Event{CreatedAt: t0}
	for attempt, want := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
		next, ok := nextAttempt(ev, attempt, t0)
		if !ok || next.Sub(t0) != want {
			t.Fatalf("attempt %d: next = %v", attempt, next.Sub(t0))
		}
	}
}

func TestNextAttemptGivesUpAfter72Hours(t *testing.T) {
	if _, ok := nextAttempt(Event{CreatedAt: t0}, 3, t0.Add(72*time.Hour)); ok {
		t.Fatal("expected no further attempt")
	}
}
""", CT, CTR),
    file("ct-doc-webhook", "documentation", "docs/architecture/webhooks.md", """
# Webhooks architecture

Events are written to an outbox table in the same transaction as the state change. The dispatcher reads the outbox, signs each payload with the merchant's secret and delivers it. Failed deliveries stay in the outbox with their next attempt time, so restarts do not lose retries.
""", CT, CTR),

    # Topic: card tokenization
    file("ct-req-token", "requirement", "docs/requirements/REQ-008-card-tokenization.md", """
# REQ-008 Card tokenization

## Requirement
Card numbers never reach Contoso's application servers. The checkout page collects card data in a hosted field and exchanges it for a token.

## Acceptance criteria
- Tokens are scoped to one merchant and cannot be used by another merchant.
- A token for a stored card can be reused for later payments; a one-time token expires after 15 minutes.
- PCI DSS scope is limited to the tokenization vault.
""", CT, CTR),
    file("ct-tc-token", "test_case", "docs/test-cases/TC-017-token-merchant-scope.md", """
# TC-017 Token cannot be used by another merchant

| Step | Action | Expected result |
|---|---|---|
| 1 | Create a card token as merchant A | Token tok_... is returned |
| 2 | Authorise a payment as merchant B with that token | Rejected with token_not_found |
""", CT, CTR),
    wi("ct-pbi-token", "work_item", "Support network tokens for stored cards", "Product Backlog Item", "New", """
Visa and Mastercard network tokens improve authorisation rates and keep stored cards valid after reissue. Request a network token when a card is stored and prefer it for merchant-initiated payments.
""", CT, area="Contoso\\Payments"),
    file("ct-code-token", "code", "internal/vault/tokens.go", """
package vault

// Resolve returns the card behind token for merchantID. Tokens are scoped to
// the merchant that created them; one-time tokens expire after 15 minutes.
func (v *Vault) Resolve(ctx context.Context, merchantID, token string) (*Card, error) {
	t, err := v.store.Get(ctx, token)
	if err != nil || t.MerchantID != merchantID {
		return nil, ErrTokenNotFound
	}
	if t.OneTime && v.clock.Since(t.CreatedAt) > 15*time.Minute {
		return nil, ErrTokenExpired
	}
	return v.decrypt(ctx, t)
}
""", CT, CTR),
    file("ct-test-token", "test_code", "internal/vault/tokens_test.go", """
package vault

func TestResolveRejectsTokenOfAnotherMerchant(t *testing.T) {
	v := newTestVault(t)
	tok := v.mustTokenize(t, "merchant-a", testCard)
	if _, err := v.Resolve(ctx, "merchant-b", tok); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("err = %v", err)
	}
}
""", CT, CTR),
    file("ct-doc-token", "documentation", "docs/architecture/pci-scope.md", """
# PCI DSS scope

Only the vault service and its database are in PCI DSS scope. The checkout page embeds hosted fields served by the vault, so card numbers go straight from the browser to the vault. Every other service sees tokens only. Access to the vault database requires break-glass approval.
""", CT, CTR),

    # Topic: monthly statements
    file("ct-req-statement", "requirement", "docs/requirements/REQ-041-monthly-statements.md", """
# REQ-041 Monthly merchant statements

## Requirement
On the first business day of each month every merchant receives a statement of the previous month: captured volume, refunds, chargebacks, fees and the net payout, per currency.

## Acceptance criteria
- Statements are available as PDF and CSV in the dashboard.
- Totals reconcile exactly with the payouts of that month.
""", CT, CTR),
    file("ct-tc-statement", "test_case", "docs/test-cases/TC-060-statement-totals.md", """
# TC-060 Statement totals reconcile with payouts

| Step | Action | Expected result |
|---|---|---|
| 1 | Generate the March statement for test merchant M-17 | PDF and CSV are produced |
| 2 | Sum the payouts of March for M-17 | Equals the statement's net payout per currency |
""", CT, CTR),
    wi("ct-bug-statement", "work_item", "Statement generation times out for merchants with over a million transactions", "Bug", "Active", """
The statement job loads all transactions of the month into memory and times out after 15 minutes for the largest merchants, who then receive no statement. Stream aggregates from the database instead.
""", CT, area="Contoso\\Reporting"),
    file("ct-code-statement", "code", "internal/statements/generator.go", """
package statements

// Generate builds a merchant's statement for month by aggregating captures,
// refunds, chargebacks and fees per currency.
func (g *Generator) Generate(ctx context.Context, merchantID string, month Month) (*Statement, error) {
	rows, err := g.db.AggregateByCurrency(ctx, merchantID, month.Start(), month.End())
	if err != nil {
		return nil, err
	}
	st := &Statement{MerchantID: merchantID, Month: month}
	for _, r := range rows {
		st.Lines = append(st.Lines, Line{Currency: r.Currency, Captured: r.Captured, Refunded: r.Refunded,
			Chargebacks: r.Chargebacks, Fees: r.Fees, Net: r.Captured - r.Refunded - r.Chargebacks - r.Fees})
	}
	return st, nil
}
""", CT, CTR),
    file("ct-test-statement", "test_code", "internal/statements/generator_test.go", """
package statements

func TestGenerateNetEqualsCapturedMinusDeductions(t *testing.T) {
	g := newTestGenerator(aggregate("EUR", captured(1000), refunded(100), chargebacks(50), fees(20)))
	st, err := g.Generate(ctx, "M-17", march)
	if err != nil {
		t.Fatal(err)
	}
	if st.Lines[0].Net != 830 {
		t.Fatalf("net = %d, want 830", st.Lines[0].Net)
	}
}
""", CT, CTR),
    file("ct-doc-statement", "documentation", "docs/runbooks/statement-generation.md", """
# Runbook: monthly statement generation

The statement job runs at 03:00 UTC on the first business day of the month. If merchants report a missing statement, check the job's dashboard for failed merchants and re-run them with `statements rerun --merchant <id> --month <yyyy-mm>`. Large merchants can take up to 40 minutes.
""", CT, CTR),
]

ct_queries = [
    q("ct-q1", "requirement", "Up to how many days after capture can a payment be refunded?", ["ct-req-refund"]),
    q("ct-q2", "test_code", "Unit test that rejects refunds above the refundable balance", ["ct-test-refund"]),
    q("ct-q3", "work_item", "Is there a backlog item to extend the refund window for enterprise merchants?", ["ct-pbi-refund"]),
    q("ct-q4", "test_case", "Test case for authorising with a stale exchange rate", ["ct-tc-fx"]),
    q("ct-q5", "code", "Where do we reject FX rates that are too old?", ["ct-code-fx"]),
    q("ct-q6", "documentation", "What should on-call do when the FX rate feed is down?", ["ct-doc-fx"]),
    q("ct-q7", "requirement", "How long are webhook deliveries retried?", ["ct-req-webhook"]),
    q("ct-q8", "documentation", "How does the webhook dispatcher survive restarts?", ["ct-doc-webhook"]),
    q("ct-q9", "test_code", "Tests for webhook retry backoff timing", ["ct-test-webhook"]),
    q("ct-q10", "test_case", "Manual test proving a card token cannot be used by a different merchant", ["ct-tc-token"]),
    q("ct-q11", "work_item", "Work item about network tokens for stored cards", ["ct-pbi-token"]),
    q("ct-q12", "code", "Implementation of resolving a card token for a merchant", ["ct-code-token"]),
    q("ct-q13", "requirement", "What must a monthly merchant statement contain?", ["ct-req-statement"]),
    q("ct-q14", "test_case", "How do we test that statement totals match payouts?", ["ct-tc-statement"]),
    q("ct-q15", "test_code", "Unit test for the statement net payout calculation", ["ct-test-statement"]),
]

# ── Fabrikam: GitHub organization with issue types ────────────────────────────
FB, FBR = "fabrikam", "field-app"
fb_docs = [
    # Topic: offline sync
    issue("fb-req-offline", "requirement", 88, "Work orders must be usable offline", "Feature", "open", ["requirement", "mobile"], """
Field technicians often have no signal on site. The app must:
- show all work orders assigned for the next 7 days without a connection,
- let technicians complete checklists and capture signatures offline,
- sync queued changes automatically within 2 minutes of reconnecting, oldest first.
Conflicting edits made by the office are resolved in favour of the office and the technician is notified.
""", FB, FBR),
    file("fb-tc-offline", "test_case", "qa/test-cases/offline-sync.md", """
# Offline sync: queued changes are sent after reconnecting

1. Put the device in airplane mode.
2. Complete the checklist of work order WO-1001 and capture a signature.
3. Disable airplane mode.
Expected: within 2 minutes the work order shows "Completed" in the office portal and the queue badge disappears.
""", FB, FBR),
    issue("fb-bug-offline", "work_item", 131, "Signature lost when the app is killed while offline", "Bug", "open", ["mobile", "data-loss"], """
If Android kills the app in the background before reconnecting, the queued signature is gone. The offline queue is only held in memory until the next flush. Persist the queue in SQLite.
""", FB, FBR),
    file("fb-code-offline", "code", "src/sync/offlineQueue.ts", """
export class OfflineQueue {
  constructor(private store: QueueStore, private api: FieldApi) {}

  async enqueue(change: Change): Promise<void> {
    await this.store.append({ ...change, queuedAt: Date.now() });
  }

  /** Sends queued changes oldest first; office edits win on conflict. */
  async flush(): Promise<FlushResult> {
    const pending = await this.store.all();
    for (const change of pending.sort((a, b) => a.queuedAt - b.queuedAt)) {
      const res = await this.api.push(change);
      if (res.status === 'conflict') notifyConflict(change, res.serverVersion);
      await this.store.remove(change.id);
    }
    return { sent: pending.length };
  }
}
""", FB, FBR),
    file("fb-test-offline", "test_code", "src/sync/offlineQueue.spec.ts", """
describe('OfflineQueue', () => {
  it('flushes changes oldest first', async () => {
    const api = fakeApi();
    const queue = new OfflineQueue(memoryStore(), api);
    await queue.enqueue(change('b', 2));
    await queue.enqueue(change('a', 1));
    await queue.flush();
    expect(api.pushedIds()).toEqual(['a', 'b']);
  });

  it('notifies the technician when the office edit wins', async () => {
    const api = fakeApi({ conflictOn: 'wo-1' });
    await new OfflineQueue(memoryStore(), api).flush();
    expect(notifications()).toContain('wo-1');
  });
});
""", FB, FBR),
    file("fb-doc-offline", "documentation", "docs/guides/offline-mode.md", """
# Offline mode

The app downloads the technician's work orders for the next seven days whenever it has a connection. Changes made offline are stored in a local queue and sent when the device is back online. A badge on the sync icon shows how many changes are waiting. If the office changed the same work order, the office version is kept.
""", FB, FBR),

    # Topic: push notifications
    issue("fb-req-push", "requirement", 54, "Push notification when a work order is assigned", "Feature", "closed", ["requirement", "notifications"], """
Technicians must receive a push notification within 1 minute when a work order is assigned to them or rescheduled. Tapping the notification opens the work order. Notifications are not sent between 22:00 and 06:00 local time unless the work order is marked urgent.
""", FB, FBR),
    file("fb-tc-push", "test_case", "qa/test-cases/push-quiet-hours.md", """
# Push notifications respect quiet hours

1. Set the technician's device time to 23:00.
2. Assign a normal-priority work order to the technician.
Expected: no notification is shown; the work order appears in the list.
3. Assign an urgent work order.
Expected: a notification is shown immediately.
""", FB, FBR),
    issue("fb-task-push", "work_item", 140, "Migrate push sending to the new FCM HTTP v1 API", "Task", "open", ["notifications", "tech-debt"], """
The legacy FCM API is shut down in June. Move the notification sender to HTTP v1 with OAuth service account credentials and update the payload format.
""", FB, FBR),
    file("fb-code-push", "code", "src/notifications/assignmentNotifier.ts", """
const QUIET_START = 22;
const QUIET_END = 6;

export function shouldNotify(order: WorkOrder, technicianLocalHour: number): boolean {
  if (order.priority === 'urgent') return true;
  const quiet = technicianLocalHour >= QUIET_START || technicianLocalHour < QUIET_END;
  return !quiet;
}

export async function notifyAssignment(order: WorkOrder, tech: Technician, push: PushSender) {
  if (!shouldNotify(order, tech.localHour())) return;
  await push.send(tech.deviceToken, { title: 'New work order', deepLink: `fieldapp://orders/${order.id}` });
}
""", FB, FBR),
    file("fb-test-push", "test_code", "src/notifications/assignmentNotifier.spec.ts", """
describe('shouldNotify', () => {
  it('suppresses normal orders during quiet hours', () => {
    expect(shouldNotify(order('normal'), 23)).toBe(false);
    expect(shouldNotify(order('normal'), 5)).toBe(false);
  });
  it('always notifies urgent orders', () => {
    expect(shouldNotify(order('urgent'), 23)).toBe(true);
  });
});
""", FB, FBR),
    file("fb-doc-push", "documentation", "docs/guides/notifications.md", """
# Notifications

Technicians get a push notification when a work order is assigned or rescheduled. Between 22:00 and 06:00 only urgent work orders notify. Technicians can mute notifications for a day from Settings; dispatchers see a muted icon next to the technician's name.
""", FB, FBR),

    # Topic: photo upload
    issue("fb-req-photo", "requirement", 61, "Attach photos to work orders", "Feature", "open", ["requirement", "mobile"], """
Technicians must be able to attach up to 20 photos per work order. Photos are compressed to at most 1600 px on the long side and uploaded in the background, also over mobile data. Uploads resume after interruptions without restarting from zero.
""", FB, FBR),
    file("fb-tc-photo", "test_case", "qa/test-cases/photo-upload-resume.md", """
# Photo upload resumes after losing connection

1. Attach five photos to a work order on a throttled 3G connection.
2. Disable the network when the second photo is half uploaded, wait 30 seconds, enable it again.
Expected: the second photo continues from where it stopped and all five photos appear in the office portal.
""", FB, FBR),
    issue("fb-bug-photo", "work_item", 152, "Photos rotated 90 degrees after upload on iOS", "Bug", "open", ["ios"], """
Portrait photos taken on iPhone appear sideways in the office portal. The compression step drops the EXIF orientation tag. Apply the orientation to the pixels before compressing.
""", FB, FBR),
    file("fb-code-photo", "code", "src/media/photoUploader.ts", """
const MAX_EDGE_PX = 1600;
const CHUNK_BYTES = 256 * 1024;

export async function uploadPhoto(photo: LocalPhoto, api: UploadApi): Promise<void> {
  const compressed = await resize(photo, MAX_EDGE_PX);
  const session = await api.startOrResume(photo.id, compressed.byteLength);
  for (let offset = session.receivedBytes; offset < compressed.byteLength; offset += CHUNK_BYTES) {
    await api.putChunk(session.id, offset, compressed.slice(offset, offset + CHUNK_BYTES));
  }
  await api.complete(session.id);
}
""", FB, FBR),
    file("fb-test-photo", "test_code", "src/media/photoUploader.spec.ts", """
describe('uploadPhoto', () => {
  it('resumes from the bytes the server already received', async () => {
    const api = fakeUploadApi({ receivedBytes: 256 * 1024 });
    await uploadPhoto(localPhoto(600 * 1024), api);
    expect(api.chunkOffsets()).toEqual([262144, 524288]);
  });
});
""", FB, FBR),
    file("fb-doc-photo", "documentation", "docs/guides/photos.md", """
# Photos

Photos attached to a work order upload in the background. The upload continues when the app is in the background and resumes after a lost connection. Photos are shown in the office portal under the work order's Attachments tab, newest first.
""", FB, FBR),

    # Topic: route planning
    issue("fb-req-route", "requirement", 73, "Daily route optimised by travel time", "Feature", "open", ["requirement", "dispatch"], """
Each morning the app must propose an order for the technician's work orders that minimises total travel time, respecting fixed appointment windows. Technicians may reorder manually; the office sees the technician's chosen order.
""", FB, FBR),
    file("fb-tc-route", "test_case", "qa/test-cases/route-appointment-windows.md", """
# Route respects fixed appointment windows

1. Assign three work orders, one with a fixed window 09:00-10:00 at the far end of town.
2. Open Today's route.
Expected: the fixed-window order is scheduled between 09:00 and 10:00 even if another order would be closer first.
""", FB, FBR),
    issue("fb-task-route", "work_item", 166, "Cache travel time matrix per day", "Task", "open", ["dispatch", "performance"], """
Route planning calls the maps API for every pair of stops on each reorder. Cache the travel time matrix per technician per day to stay within the maps API quota.
""", FB, FBR),
    file("fb-code-route", "code", "src/routing/planRoute.ts", """
export function planRoute(stops: Stop[], matrix: TravelMatrix, start: Date): PlannedStop[] {
  const fixed = stops.filter((s) => s.window).sort((a, b) => a.window!.start - b.window!.start);
  const flexible = stops.filter((s) => !s.window);
  // Greedy: insert each flexible stop where it adds the least travel time
  // without pushing any fixed stop out of its window.
  let route = fixed.map((s) => ({ stop: s }));
  for (const stop of flexible) {
    route = cheapestFeasibleInsertion(route, stop, matrix, start);
  }
  return schedule(route, matrix, start);
}
""", FB, FBR),
    file("fb-test-route", "test_code", "src/routing/planRoute.spec.ts", """
describe('planRoute', () => {
  it('keeps fixed appointment windows', () => {
    const plan = planRoute([stop('a'), fixedStop('b', '09:00', '10:00'), stop('c')], matrix, eightAm);
    const b = plan.find((p) => p.stop.id === 'b')!;
    expect(b.arrival >= nineAm && b.arrival <= tenAm).toBe(true);
  });
});
""", FB, FBR),
    file("fb-doc-route", "documentation", "docs/guides/route-planning.md", """
# Route planning

Today's route lists the technician's work orders in a suggested order based on travel time. Orders with a fixed appointment window are always placed inside their window. Technicians can drag orders to change the sequence; dispatchers see the updated order on the map.
""", FB, FBR),

    # Topic: timesheet approval
    issue("fb-req-timesheet", "requirement", 95, "Weekly timesheet approval by team leads", "Feature", "open", ["requirement", "payroll"], """
Technicians submit their timesheet for the week by Monday 10:00. Team leads approve or reject each timesheet with a comment. Approved timesheets are exported to payroll every Monday at 18:00; rejected ones go back to the technician.
""", FB, FBR),
    file("fb-tc-timesheet", "test_case", "qa/test-cases/timesheet-rejection.md", """
# Rejected timesheet returns to the technician

1. Submit a timesheet as technician T-4.
2. As team lead, reject it with the comment "Missing Tuesday".
Expected: T-4 sees the timesheet as editable with the comment; it is not part of the payroll export.
""", FB, FBR),
    issue("fb-bug-timesheet", "work_item", 171, "Payroll export includes timesheets rejected after 18:00", "Bug", "open", ["payroll"], """
A timesheet approved and then rejected on Monday after 18:00 was still exported to payroll, because the export reads approvals without checking the latest status.
""", FB, FBR),
    file("fb-code-timesheet", "code", "src/timesheets/payrollExport.ts", """
export async function exportApprovedTimesheets(week: IsoWeek, repo: TimesheetRepo, payroll: PayrollClient) {
  const sheets = await repo.forWeek(week);
  const approved = sheets.filter((s) => latestStatus(s) === 'approved');
  await payroll.submit(approved.map(toPayrollRecord));
  await repo.markExported(approved.map((s) => s.id));
}
""", FB, FBR),
    file("fb-test-timesheet", "test_code", "src/timesheets/payrollExport.spec.ts", """
describe('exportApprovedTimesheets', () => {
  it('skips timesheets whose latest status is rejected', async () => {
    const repo = fakeRepo([sheet('t1', ['approved', 'rejected']), sheet('t2', ['approved'])]);
    const payroll = fakePayroll();
    await exportApprovedTimesheets(week23, repo, payroll);
    expect(payroll.submittedIds()).toEqual(['t2']);
  });
});
""", FB, FBR),
    file("fb-doc-timesheet", "documentation", "docs/guides/timesheets.md", """
# Timesheets

Technicians record hours per work order; the app fills in travel time automatically. Submit the week's timesheet by Monday 10:00. Your team lead approves it or sends it back with a comment. Approved hours are paid with the next payroll run.
""", FB, FBR),
]

fb_queries = [
    q("fb-q1", "requirement", "How quickly must offline changes be synced after reconnecting?", ["fb-req-offline"]),
    q("fb-q2", "test_code", "Unit tests for the offline queue ordering", ["fb-test-offline"]),
    q("fb-q3", "work_item", "Is the lost signature problem when the app is killed offline tracked?", ["fb-bug-offline"]),
    q("fb-q4", "test_case", "QA steps for push notifications during quiet hours", ["fb-tc-push"]),
    q("fb-q5", "code", "Where is the quiet hours check for push notifications?", ["fb-code-push"]),
    q("fb-q6", "documentation", "User guide about muting notifications", ["fb-doc-push"]),
    q("fb-q7", "requirement", "How many photos can be attached to a work order?", ["fb-req-photo"]),
    q("fb-q8", "documentation", "Where do uploaded photos show up in the office portal?", ["fb-doc-photo"]),
    q("fb-q9", "test_code", "Test that a photo upload resumes from the received bytes", ["fb-test-photo"]),
    q("fb-q10", "test_case", "Test case: route planning with a fixed appointment window", ["fb-tc-route"]),
    q("fb-q11", "work_item", "Any task about the maps API quota for route planning?", ["fb-task-route"]),
    q("fb-q12", "code", "Route optimisation implementation", ["fb-code-route"]),
    q("fb-q13", "requirement", "When are approved timesheets exported to payroll?", ["fb-req-timesheet"]),
    q("fb-q14", "test_case", "What happens in QA when a team lead rejects a timesheet?", ["fb-tc-timesheet"]),
    q("fb-q15", "test_code", "Test that rejected timesheets are not exported", ["fb-test-timesheet"]),
]

dataset = {
    "name": "synthetic",
    "description": "Three fictional companies with different ways of organizing requirements, test cases, work items, code, tests and docs. Each topic has one document of every intent, so same-topic documents of other kinds compete in search.",
    "companies": [
        {
            "id": "northwind",
            "description": "Azure DevOps, custom inherited process: requirements and test cases are work items (Product Requirement, Test Case). C# with tests in a separate tests/ project. Docs in the project wiki.",
            "scopes": {
                "requirement": [{"collection": "work_items", "filters": {"work_item_type": ["Product Requirement"]}}],
                "test_case": [{"collection": "work_items", "filters": {"work_item_type": ["Test Case"]}}],
                "work_item": [{"collection": "work_items", "filters": {"work_item_type": ["Bug", "User Story", "Task"]}}],
                "code": [{"collection": "code", "filters": {"code_kind": ["production"]}}],
                "test_code": [{"collection": "code", "filters": {"code_kind": ["test"]}}],
                "documentation": [{"collection": "documentation"}],
            },
            "documents": nw_docs,
            "queries": nw_queries,
        },
        {
            "id": "contoso",
            "description": "Azure DevOps Scrum process: requirements and test cases are Markdown files in the repository (docs/requirements, docs/test-cases); work items are Product Backlog Items and Bugs. Go with tests beside the code (*_test.go).",
            "scopes": {
                "requirement": [{"collection": "documentation", "filters": {"path": ["docs/requirements"]}}],
                "test_case": [{"collection": "documentation", "filters": {"path": ["docs/test-cases"]}}],
                "work_item": [{"collection": "work_items"}],
                "code": [{"collection": "code", "filters": {"code_kind": ["production"]}}],
                "test_code": [{"collection": "code", "filters": {"code_kind": ["test"]}}],
                "documentation": [{"collection": "documentation", "filters": {"path": ["docs/architecture", "docs/runbooks"]}}],
            },
            "documents": ct_docs,
            "queries": ct_queries,
        },
        {
            "id": "fabrikam",
            "description": "GitHub organization with issue types: requirements are Feature issues labelled 'requirement', bugs and tasks are issues of those types. Test cases are Markdown files in qa/test-cases. TypeScript with *.spec.ts beside the code.",
            "scopes": {
                "requirement": [{"collection": "work_items", "filters": {"tags": ["requirement"]}}],
                "test_case": [{"collection": "documentation", "filters": {"path": ["qa/test-cases"]}}],
                "work_item": [{"collection": "work_items", "filters": {"work_item_type": ["Bug", "Task"]}}],
                "code": [{"collection": "code", "filters": {"code_kind": ["production"]}}],
                "test_code": [{"collection": "code", "filters": {"code_kind": ["test"]}}],
                "documentation": [{"collection": "documentation", "filters": {"path": ["docs"]}}],
            },
            "documents": fb_docs,
            "queries": fb_queries,
        },
    ],
}

out = sys.argv[1]
with open(out, "w", encoding="utf-8", newline="\n") as f:
    json.dump(dataset, f, indent=2, ensure_ascii=False)
    f.write("\n")
print(f"wrote {out}: {sum(len(c['documents']) for c in dataset['companies'])} documents, "
      f"{sum(len(c['queries']) for c in dataset['companies'])} queries")
