
export interface IP {
  id: number;
  name: string;
  name_en?: string;
  name_cn?: string;
  image_url?: string;
  category?: string;
  tags?: string[];
  weight?: number;
}

export interface IPStats {
  id: number;
  inflow: number;
  outflow: number;
  score: number;
  rank?: number;
}

export interface Item {
  id: string | number;
  source_id?: string;
  title: string;
  ip_id?: number;
  category?: string;
  keywords?: string[];
  price: number;
  item_url: string;
  image_url: string;
  status: 'on_sale' | 'sold';
  first_seen?: string;
  last_seen?: string;
}

export type TimeRange = 2 | 24 | 168;
