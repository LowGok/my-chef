package com.gkldevelopment.mychef.preview;

final class PlaySubscriptionTerms {
    private PlaySubscriptionTerms() {}

    static boolean matches(String interval, String country, String currency, long priceMicros,
                           String period, String offer, boolean installment, int phases, boolean recurring) {
        if (!"BE".equals(country) || !"EUR".equals(currency) || offer != null
                || installment || phases != 1 || !recurring) return false;
        return ("monthly".equals(interval) && "P1M".equals(period) && priceMicros == 6_990_000)
                || ("annual".equals(interval) && "P1Y".equals(period) && priceMicros == 69_900_000);
    }
}
