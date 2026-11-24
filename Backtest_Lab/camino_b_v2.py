"""CAMINO B v2: Fix de fechas"""
import os, warnings
import numpy as np, pandas as pd, yfinance as yf
warnings.filterwarnings("ignore")
CACHE_DIR = "data_cache"; os.makedirs(CACHE_DIR, exist_ok=True)

def to_date_df(df):
    df = df.copy().reset_index()
    if "date" not in df.columns: df.rename(columns={df.columns[0]: "date"}, inplace=True)
    return df

def download(symbol, interval="1d"):
    key = f"{symbol.replace('=','_').replace('^','')}_{interval}_max_now"
    cache = os.path.join(CACHE_DIR, key + ".csv")
    if os.path.exists(cache): return pd.read_csv(cache, index_col=0, parse_dates=True)
    print(f"  [desc] {symbol}...", end=" ")
    df = yf.download(symbol, period="max", interval=interval, progress=False, auto_adjust=False, threads=False)
    if df is None or df.empty: print("vacio"); return pd.DataFrame()
    if isinstance(df.columns, pd.MultiIndex): df.columns = df.columns.get_level_values(0)
    df.columns = [c.lower() for c in df.columns]
    df = df[["open","high","low","close","volume"]].dropna()
    df.index.name = "date"; df.to_csv(cache); print(f"{len(df)} velas"); return df

def metrics(eq, capital, periods=252):
    r = eq.pct_change().dropna()
    if len(r) < 10: return None
    y = (eq.index[-1] - eq.index[0]).days / 365.25
    cagr = (eq.iloc[-1]/eq.iloc[0])**(1/y) - 1 if y > 0 else 0
    sr = r.mean()/r.std()*np.sqrt(periods) if r.std() > 0 else 0
    peak = eq.cummax(); dd = (eq - peak)/peak
    return {"sharpe": sr, "cagr": cagr*100, "mdd": dd.min()*100, "final": eq.iloc[-1]}

def backtest_raschke_vix(df, df_vix, capital=10000, risk_pct=1.0):
    df = to_date_df(df)
    df_vix = to_date_df(df_vix)
    
    # Unir VIX con QQQ
    df = df.merge(df_vix[["date", "close"]], on="date", how="left", suffixes=("", "_vix"))
    df["vix"] = df["close_vix"].ffill()
    
    df["ib_high"] = df["high"].shift(1); df["ib_low"] = df["low"].shift(1)
    df["ib_mid"] = (df["ib_high"]+df["ib_low"])/2
    
    # Filtro: Solo opera si el VIX de ayer fue > 18
    df["trade_allowed"] = df["vix"].shift(1) > 18.0
    
    sigs = []
    for i in range(1, len(df)):
        r = df.iloc[i]
        if pd.isna(r["ib_high"]) or pd.isna(r["vix"]): continue
        p, ih, il, im = r["close"], r["ib_high"], r["ib_low"], r["ib_mid"]
        
        if r["trade_allowed"]:
            if p >= ih*0.999: sigs.append({"idx": i, "dir": 1, "entry": ih, "sl": im, "tp": ih+(ih-il)*0.5})
            elif p <= il*1.001: sigs.append({"idx": i, "dir": -1, "entry": il, "sl": im, "tp": il-(ih-il)*0.5})
            
    nav = capital; equity = []; open_t = None
    sig_by_day = {}
    for s in sigs: sig_by_day.setdefault(s["idx"], []).append(s)
    
    for i in range(len(df)):
        r = df.iloc[i]
        if open_t is not None:
            t = open_t; exit_px = None
            if t["dir"] == 1:
                if r["low"] <= t["sl"]: exit_px = t["sl"]
                elif r["high"] >= t["tp"]: exit_px = t["tp"]
            else:
                if r["high"] >= t["sl"]: exit_px = t["sl"]
                elif r["low"] <= t["tp"]: exit_px = t["tp"]
            if exit_px is None and (i-t["entry_idx"]) >= 5: exit_px = r["close"]
            if exit_px is not None:
                ex = exit_px * (1 - 0.0002 * t["dir"])
                pnl = (ex - t["entry_px"]) * t["dir"] * t["qty"] - 0.005*t["qty"]*2
                nav += pnl; open_t = None
                
        if open_t is None and i in sig_by_day:
            s = sig_by_day[i][0]
            entry_px = s["entry"] * (1 + 0.0002 * s["dir"])
            risk_dollars = nav * risk_pct/100
            sl_dist = abs(entry_px - s["sl"])
            if sl_dist > 0:
                qty = int(min(risk_dollars/sl_dist, (nav*0.15)/entry_px))
                if qty >= 1:
                    open_t = {"dir": s["dir"], "entry_px": entry_px, "sl": s["sl"], "tp": s["tp"], "qty": qty, "entry_idx": i}
        equity.append({"date": df.iloc[i]["date"], "nav": nav})
        
    eq = pd.Series([e["nav"] for e in equity], index=[e["date"] for e in equity])
    return metrics(eq, capital)

def main():
    print("="*75); print("  CAMINO B v2: RASCHKE + FILTRO VIX"); print("="*75)
    capital = 10000

    qqq = download("QQQ", "1d")
    vix = download("^VIX", "1d")

    print("\n[RASCHKE + Filtro VIX > 18]")
    m = backtest_raschke_vix(qqq, vix, capital)
    if m: print(f"  Sharpe {m['sharpe']:.2f} | CAGR {m['cagr']:.2f}% | DD {m['mdd']:.2f}% | NAV ${m['final']:,.0f}")

    print("\n" + "="*75)
    print("  Comparar contra Raschke normal (Sharpe 3.48)")
    print("  Si este Sharpe es MAYOR a 3.48, el filtro VIX mejora el edge.")

if __name__ == "__main__": main()