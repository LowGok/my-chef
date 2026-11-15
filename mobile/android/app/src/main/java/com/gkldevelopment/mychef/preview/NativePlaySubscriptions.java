package com.gkldevelopment.mychef.preview;

import android.app.Activity;
import android.content.Context;
import android.os.Handler;
import android.os.Looper;
import com.android.billingclient.api.AcknowledgePurchaseParams;
import com.android.billingclient.api.BillingClient;
import com.android.billingclient.api.BillingClientStateListener;
import com.android.billingclient.api.BillingFlowParams;
import com.android.billingclient.api.BillingResult;
import com.android.billingclient.api.GetBillingConfigParams;
import com.android.billingclient.api.PendingPurchasesParams;
import com.android.billingclient.api.ProductDetails;
import com.android.billingclient.api.Purchase;
import com.android.billingclient.api.QueryProductDetailsParams;
import com.android.billingclient.api.QueryPurchasesParams;
import com.getcapacitor.JSArray;
import com.getcapacitor.JSObject;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashSet;
import java.util.List;
import java.util.Set;
import java.util.function.Consumer;
import org.json.JSONObject;

final class NativePlaySubscriptions {
    private static final String SUBS = BillingClient.ProductType.SUBS;
    private final Handler main = new Handler(Looper.getMainLooper());
    private final BillingClient client;
    private final List<Consumer<Boolean>> waiting = new ArrayList<>();
    private boolean connecting;
    private boolean closed;
    private Pending pending;
    private boolean sheetLaunched;

    private record Plan(String product, String base, String interval) {
        static Plan read(JSONObject data) {
            if (data == null) return null;
            var product = data.optString("product", "");
            var base = data.optString("basePlan", "");
            var interval = data.optString("interval", "");
            if (!product.matches("[a-z0-9][a-z0-9_.-]{0,127}") || !base.matches("[a-z0-9][a-z0-9-]{0,62}")
                    || !(interval.equals("monthly") || interval.equals("annual"))) return null;
            return new Plan(product, base, interval);
        }
    }
    private record Offer(Plan plan, ProductDetails details, String token, String price) {
        JSObject quote() {
            return new JSObject().put("store", "play").put("region", "BE").put("currency", "EUR")
                    .put("product", plan.product).put("basePlan", plan.base).put("interval", plan.interval)
                    .put("amountMinor", plan.interval.equals("monthly") ? 699 : 6990)
                    .put("displayPrice", price).put("introductory", false);
        }
    }
    private record Pending(Plan plan, String binding, Consumer<JSObject> respond) {}

    NativePlaySubscriptions(Context context) {
        client = BillingClient.newBuilder(context.getApplicationContext())
                .setListener((result, purchases) -> main.post(() -> purchaseUpdate(result, purchases)))
                .enablePendingPurchases(PendingPurchasesParams.newBuilder().enableOneTimeProducts().build())
                .enableAutoServiceReconnection()
                .build();
    }

    private static boolean ok(BillingResult result) {
        return result.getResponseCode() == BillingClient.BillingResponseCode.OK;
    }

    private void withConnection(Consumer<Boolean> run) {
        if (closed || client.isReady()) {
            run.accept(!closed);
            return;
        }
        waiting.add(run);
        if (connecting) return;
        connecting = true;
        client.startConnection(new BillingClientStateListener() {
            @Override public void onBillingSetupFinished(BillingResult result) {
                main.post(() -> resolveConnection(ok(result)));
            }
            @Override public void onBillingServiceDisconnected() {
                main.post(() -> resolveConnection(false));
            }
        });
    }

    private void resolveConnection(boolean ready) {
        connecting = false;
        var queued = new ArrayList<>(waiting);
        waiting.clear();
        for (var run : queued) run.accept(ready && !closed);
    }

    private void eligibleOffers(List<Plan> plans, Consumer<List<Offer>> done) {
        if (plans.isEmpty()) {
            done.accept(Collections.emptyList());
            return;
        }
        withConnection(connected -> {
            if (!connected) { done.accept(Collections.emptyList()); return; }
            client.getBillingConfigAsync(GetBillingConfigParams.newBuilder().build(),
                    (result, config) -> main.post(() -> {
                if (closed || !ok(result) || config == null || !"BE".equals(config.getCountryCode())) {
                    done.accept(Collections.emptyList()); return;
                }
                Set<String> seen = new HashSet<>();
                List<QueryProductDetailsParams.Product> products = new ArrayList<>();
                for (var plan : plans) {
                    if (seen.add(plan.product)) products.add(QueryProductDetailsParams.Product.newBuilder()
                            .setProductId(plan.product).setProductType(SUBS).build());
                }
                client.queryProductDetailsAsync(QueryProductDetailsParams.newBuilder().setProductList(products).build(),
                        (query, data) -> main.post(() -> {
                    var offers = new ArrayList<Offer>();
                    if (!closed && ok(query) && data != null) {
                        for (var plan : plans) {
                            Offer match = null;
                            boolean duplicate = false;
                            for (var details : data.getProductDetailsList()) {
                                if (!plan.product.equals(details.getProductId()) || !SUBS.equals(details.getProductType())
                                        || details.getSubscriptionOfferDetails() == null) continue;
                                for (var offer : details.getSubscriptionOfferDetails()) {
                                    var phases = offer.getPricingPhases().getPricingPhaseList();
                                    if (!plan.base.equals(offer.getBasePlanId()) || phases.size() != 1) continue;
                                    var phase = phases.get(0);
                                    if (!PlaySubscriptionTerms.matches(plan.interval, config.getCountryCode(),
                                            phase.getPriceCurrencyCode(), phase.getPriceAmountMicros(),
                                            phase.getBillingPeriod(), offer.getOfferId(), offer.getInstallmentPlanDetails() != null,
                                            phases.size(), phase.getRecurrenceMode() == ProductDetails.RecurrenceMode.INFINITE_RECURRING)) continue;
                                    var price = phase.getFormattedPrice();
                                    if (price == null || price.trim().isEmpty() || price.length() > 64 || price.chars().anyMatch(c -> c < 32 || c == 127)
                                            || offer.getOfferToken() == null || offer.getOfferToken().isEmpty()) continue;
                                    if (match != null) duplicate = true;
                                    match = new Offer(plan, details, offer.getOfferToken(), price);
                                }
                            }
                            if (!duplicate && match != null) offers.add(match);
                        }
                    }
                    done.accept(offers);
                }));
            }));
        });
    }

    void quotes(JSArray input, Consumer<JSObject> respond) {
        List<Plan> plans = new ArrayList<>();
        if (input != null && input.length() <= 2) {
            for (int i = 0; i < input.length(); i++) {
                Plan plan = Plan.read(input.optJSONObject(i));
                if (plan != null && !plans.contains(plan)) plans.add(plan);
            }
        }
        eligibleOffers(plans, offers -> {
            var quotes = new JSArray();
            for (var offer : offers) quotes.put(offer.quote());
            respond.accept(new JSObject().put("quotes", quotes));
        });
    }

    private void owned(Consumer<List<Purchase>> respond) {
        withConnection(connected -> {
            if (!connected) { respond.accept(null); return; }
            client.queryPurchasesAsync(QueryPurchasesParams.newBuilder().setProductType(SUBS).build(),
                    (result, purchases) -> main.post(() -> respond.accept(!closed && ok(result) ? purchases : null)));
        });
    }

    private static boolean isBinding(String binding) {
        return binding != null && binding.matches("(?i)[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}");
    }

    private static boolean matches(Purchase purchase, Plan plan) {
        return purchase.getProducts().size() == 1 && purchase.getProducts().contains(plan.product);
    }

    void purchase(Activity activity, JSONObject input, Consumer<JSObject> respond) {
        var plan = Plan.read(input);
        var binding = input.optString("binding", "");
        var price = input.optString("displayPrice", "");
        if (closed || pending != null || plan == null || !isBinding(binding) || price.isEmpty()
                || activity == null || activity.isFinishing() || activity.isDestroyed()) {
            respond.accept(state("pending")); return;
        }
        var attempt = new Pending(plan, binding, respond);
        pending = attempt;
        main.postDelayed(() -> {
            if (pending == attempt) finishAttempt("pending", null);
        }, 120_000L);
        owned(purchases -> {
            if (pending != attempt) return;
            if (purchases == null || purchases.stream().anyMatch(p -> p.getProducts().contains(plan.product))) {
                finishAttempt("pending", null); return;
            }
            eligibleOffers(Collections.singletonList(plan), offers -> {
                if (pending != attempt) return;
                if (offers.size() != 1 || !offers.get(0).price.equals(price) || activity.isFinishing() || activity.isDestroyed()) {
                    finishAttempt("pending", null); return;
                }
                var offer = offers.get(0);
                var selected = BillingFlowParams.ProductDetailsParams.newBuilder()
                        .setProductDetails(offer.details).setOfferToken(offer.token).build();
                var result = client.launchBillingFlow(activity, BillingFlowParams.newBuilder()
                        .setProductDetailsParamsList(Collections.singletonList(selected)).setObfuscatedAccountId(binding).build());
                if (!ok(result)) finishAttempt("pending", null);
                else sheetLaunched = true;
            });
        });
    }

    private void purchaseUpdate(BillingResult result, List<Purchase> purchases) {
        if (pending == null || !sheetLaunched) return;
        if (result.getResponseCode() == BillingClient.BillingResponseCode.USER_CANCELED) {
            finishAttempt("cancelled", null); return;
        }
        if (ok(result) && purchases != null && purchases.size() == 1) {
            var purchase = purchases.get(0);
            var account = purchase.getAccountIdentifiers();
            if (matches(purchase, pending.plan) && account != null
                    && pending.binding.equalsIgnoreCase(account.getObfuscatedAccountId())) {
                var proof = receipt(purchase, true);
                if (proof != null) {
                    finishAttempt("proof", proof.put("interval", pending.plan.interval)); return;
                }
            }
        }
        finishAttempt("pending", null);
    }

    private static JSObject state(String value) { return new JSObject().put("state", value); }

    private void finishAttempt(String status, JSObject proof) {
        var attempt = pending;
        pending = null;
        sheetLaunched = false;
        if (attempt != null) {
            var event = state(status);
            if (proof != null) event.put("proof", proof);
            attempt.respond.accept(event);
        }
    }

    private static JSObject receipt(Purchase purchase, boolean includePending) {
        var token = purchase.getPurchaseToken();
        var account = purchase.getAccountIdentifiers();
        int state = purchase.getPurchaseState();
        boolean verifiable = state == Purchase.PurchaseState.PURCHASED
                || (includePending && state == Purchase.PurchaseState.PENDING);
        if (!verifiable || purchase.getProducts().size() != 1
                || account == null || !isBinding(account.getObfuscatedAccountId()) || token == null
                || token.length() > 2048 || !token.matches("[A-Za-z0-9._-]+") || ".".equals(token) || "..".equals(token)) return null;
        return new JSObject().put("id", handle(token)).put("receipt", token);
    }

    private static String handle(String token) {
        try {
            var hash = MessageDigest.getInstance("SHA-256").digest(("mychef/play/local/v1\0" + token).getBytes(StandardCharsets.UTF_8));
            var id = new StringBuilder(64);
            for (byte bit : hash) id.append(Character.forDigit((bit & 255) >>> 4, 16)).append(Character.forDigit(bit & 15, 16));
            return id.toString();
        } catch (NoSuchAlgorithmException error) { throw new IllegalStateException("SHA-256 unavailable"); }
    }

    void receipts(boolean unfinished, Consumer<JSObject> respond) {
        owned(purchases -> {
            if (purchases == null || purchases.size() > 32) { respond.accept(null); return; }
            var receipts = new JSArray();
            for (var purchase : purchases) {
                var value = receipt(purchase, unfinished);
                if (value != null && (!unfinished || !purchase.isAcknowledged())) receipts.put(value);
            }
            respond.accept(new JSObject().put("receipts", receipts));
        });
    }

    void finish(String id, Consumer<JSObject> respond) {
        if (id == null || !id.matches("[0-9a-f]{64}")) { respond.accept(null); return; }
        owned(purchases -> {
            if (purchases == null) { respond.accept(null); return; }
            Purchase purchase = null;
            for (var candidate : purchases) {
                var value = receipt(candidate, false);
                if (value == null || !id.equals(value.optString("id"))) continue;
                if (purchase != null) { respond.accept(null); return; }
                purchase = candidate;
            }
            if (purchase == null) { respond.accept(null); return; }
            if (purchase.isAcknowledged()) { respond.accept(new JSObject()); return; }
            client.acknowledgePurchase(AcknowledgePurchaseParams.newBuilder().setPurchaseToken(purchase.getPurchaseToken())
                    .build(), result -> main.post(() -> respond.accept(!closed && ok(result) ? new JSObject() : null)));
        });
    }

    void close() {
        closed = true;
        main.removeCallbacksAndMessages(null);
        finishAttempt("pending", null);
        resolveConnection(false);
        client.endConnection();
    }
}
