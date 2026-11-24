"""Backtest con metricas CORRECTAS - raschke validated"""
import os, warnings
import numpy as np, pandas as pd, yfinance as yf
warnings.filterwarnings("ignore")
CACHE_DIR = "data_cache"; os.makedirs(CACHE_DIR, exist_ok=True)

def download(symbol, interval="1d"):
    key = f"{symbol.replace('=','_')}_{interval}_max_now"
    cache = os.path.join(CACHE_DIR, key + ".csv")
    if os.path.exists(cache): return pd.read_csv(cache, index_col=0, parse_dates=True)
    print(f"  [desc] {symbol} {interval}...", end=" ")
    df = yf.download(symbol, period="max", interval=interval, progress=False, auto_adjust=False, threads=False)
    if df is None or df.empty: print("vacio"); return pd.DataFrame()
    if isinstance(df.columns, pd.MultiIndex): df.columns = df.columns.get_level_values(0)
    df.columns = [c.lower() for c in df.columns]
    df = df[["open","high","low","close","volume"]].dropna()
    df.to_csv(cache); print(f"{len(df)} velas"); return df

def signals(df):
    df = df.copy()
    df["ib_high"] = df["high"].shift(1); df["ib_low"] = df["low"].shift(1)
    df["ib_mid"] = (df["ib_high"]+df["ib_low"])/2
    out = []
    for i in range(1, len(df)):
        r = df.iloc[i]
        if pd.isna(r["ib_high"]): continue
        p, ih, il, im = r["close"], r["ib_high"], r["ib_low"], r["ib_mid"]
        if p >= ih*0.999:
            out.append({"idx": i, "dir": 1, "entry": ih, "sl": im, "tp": ih+(ih-il)*0.5})
        elif p <= il*1.001:
            out.append({"idx": i, "dir": -1, "entry": il, "sl": im, "tp": il-(ih-il)*0.5})
    return out

def backtest_compounding(df, capital=10000, risk_pct=1.0, comm=0.005, slip_bps=2.0, max_bars=5):
    sigs = signals(df)
    nav = capital; equity = []; trades = []
    max_pos_pct = 15.0

    sig_by_day = {}
    for s in sigs: sig_by_day.setdefault(s["idx"], []).append(s)

    open_trade = None
    for i in range(len(df)):
        r = df.iloc[i]
        if open_trade is not None:
            t = open_trade
            exit_px = None; reason = "TIME"
            if t["dir"] == 1:
                if r["low"] <= t["sl"]: exit_px, reason = t["sl"], "SL"
                elif r["high"] >= t["tp"]: exit_px, reason = t["tp"], "TP"
            else:
                if r["high"] >= t["sl"]: exit_px, reason = t["sl"], "SL"
                elif r["low"] <= t["tp"]: exit_px, reason = t["tp"], "TP"

            if exit_px is None and (i - t["entry_idx"]) >= max_bars:
                exit_px, reason = r["close"], "TIME"

            if exit_px is not None:
                exit_with_slip = exit_px * (1 - slip_bps/10000 * t["dir"])
                pnl_gross = (exit_with_slip - t["entry_px"]) * t["dir"] * t["qty"]
                pnl_net = pnl_gross - (comm * t["qty"] * 2)
                nav += pnl_net
                trades.append({"pnl": pnl_net, "reason": reason, "dir": t["dir"]})
                open_trade = None

        if open_trade is None and i in sig_by_day:
            s = sig_by_day[i][0]
            entry_px = s["entry"] * (1 + slip_bps/10000 * s["dir"])
            risk_dollars = nav * risk_pct / 100
            sl_dist = abs(entry_px - s["sl"])
            if sl_dist > 0:
                qty_risk = risk_dollars / sl_dist
                qty_max = (nav * max_pos_pct / 100) / entry_px
                qty = int(min(qty_risk, qty_max))
                if qty >= 1:
                    open_trade = {"dir": s["dir"], "entry_px": entry_px, "sl": s["sl"], "tp": s["tp"], "qty": qty, "entry_idx": i}

        equity.append({"date": df.index[i], "nav": nav})

    eq = pd.DataFrame(equity).set_index("date")["nav"]
    if len(trades) < 5: return None, eq

    # Metricas CORRECTAS basadas en retornos diarios
    daily_ret = eq.pct_change().dropna()
    if len(daily_ret) < 20: return None, eq

    days = (eq.index[-1] - eq.index[0]).days
    years = days / 365.25
    cagr = (eq.iloc[-1]/eq.iloc[0])**(1/years) - 1 if years > 0 else 0

    sr = daily_ret.mean() / daily_ret.std() * np.sqrt(252) if daily_ret.std() > 0 else 0
    dn = daily_ret[daily_ret < 0]
    sortino = daily_ret.mean() / dn.std() * np.sqrt(252) if len(dn) > 0 and dn.std() > 0 else 0

    peak = eq.cummax(); dd = (eq - peak) / peak
    mdd = dd.min() if len(dd) > 0 else 0

    win_rate = sum(1 for t in trades if t["pnl"] > 0) / len(trades) if trades else 0
    pf = sum(t["pnl"] for t in trades if t["pnl"] > 0) / abs(sum(t["pnl"] for t in trades if t["pnl"] < 0)) if any(t["pnl"] < 0 for t in trades) else 999

    return {
        "n": len(trades), "wr": win_rate, "sharpe": sr, "sortino": sortino,
        "cagr": cagr*100, "mdd": mdd*100, "pf": pf,
        "final_nav": eq.iloc[-1], "years": years
    }, eq

def main():
    print("="*80); print("  BACKTEST CON METRICAS CORRECTAS - capital compuesto"); print("="*80)
    print("\nConfig: $10k inicial, risk 1% por trade, 15% max pos, $0.005 comm, 2bps slip\n")

    print("[QQQ diario - periodo completo]")
    df = download("QQQ", "1d")
    if not df.empty:
        m, eq = backtest_compounding(df)
        if m:
            print(f"  Periodo: {m['years']:.1f} anios | Trades: {m['n']}")
            print(f"  WR: {m['wr']*100:.1f}% | PF: {m['pf']:.2f}")
            print(f"  Sharpe REAL: {m['sharpe']:.2f} | Sortino: {m['sortino']:.2f}")
            print(f"  CAGR: {m['cagr']:.2f}% | MaxDD: {m['mdd']:.2f}%")
            print(f"  NAV final: ${m['final_nav']:,.0f} (inicio $10,000)")
            eq.to_csv("equity_qqq_1d.csv")

    print("\n[Otros activos - Sharpe REAL]")
    for tk in ["SPY","IWM","AAPL","MSFT","TSLA","NVDA","GOOGL","META"]:
        df = download(tk, "1d")
        if df.empty: continue
        m, _ = backtest_compounding(df)
        if m:
            print(f"  {tk:6s} N={m['n']:5d} WR={m['wr']*100:5.1f}% Sharpe={m['sharpe']:5.2f} CAGR={m['cagr']:6.2f}% DD={m['mdd']:6.2f}%")

    print("\n[Bear markets - NAV final desde $10k]")
    for start, end, name in [
        ("2000-01-01","2002-12-31","Dot-com 2000-02"),
        ("2007-10-01","2009-03-31","GFC 2007-09"),
        ("2022-01-01","2022-12-31","Bear 2022"),
    ]:
        print(f"  [desc] QQQ {start}->{end}...", end=" ")
        df2 = yf.download("QQQ", start=start, end=end, interval="1d",
                          progress=False, auto_adjust=False, threads=False)
        if df2 is None or df2.empty: print("vacio"); continue
        if isinstance(df2.columns, pd.MultiIndex): df2.columns = df2.columns.get_level_values(0)
        df2.columns = [c.lower() for c in df2.columns]
        df2 = df2[["open","high","low","close","volume"]].dropna()
        print(f"{len(df2)} velas")
        m, _ = backtest_compounding(df2)
        if m:
            print(f"    {name:20s} N={m['n']:4d} Sharpe={m['sharpe']:5.2f} CAGR={m['cagr']:7.2f}% NAV: ${m['final_nav']:,.0f}")

    print("\n" + "="*80)
    print("  INTERPRETACION")
    print("="*80)
    print("  Sharpe 1.0-1.5  -> edge real, estrategia decente")
    print("  Sharpe 1.5-2.5  -> edge fuerte, nivel CTA profesional")
    print("  Sharpe > 3      -> sospecha, revisar look-ahead")
    print("  Sharpe > 5      -> imposible, hay bug en la simulacion")

if __name__ == "__main__": main()